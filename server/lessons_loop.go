package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The self-improvement lesson loop (Meta Early Experience self-reflection,
// adapted to inference-time): three moving parts.
//
//  1. CONTRASTIVE EXTRACTION — after a task closes, compare its productive
//     branches against its dead ones and distill why the winners won (the
//     paper's "why the expert action beats the alternative" chain-of-thought,
//     kept as a lesson row instead of a weight update).
//  2. OUTCOME FEEDBACK — when a task that was SHOWN lessons closes, its yield
//     updates those lessons: candidate lessons used by finding-producing tasks
//     are promoted to active; lessons repeatedly seen by zero-finding tasks
//     retire. Usage is already logged (rsi_lesson_usage) in both shadow and
//     inject modes.
//  3. RUBRIC DISAGREEMENT — when the operator's status flip contradicts the
//     retester's verdict, the disagreement becomes a negative lesson for the
//     next retest round (Self-Taught Evaluators pattern, measurement half).

const contrastiveSystem = `You distill exploration lessons for an autonomous pentest platform from ONE completed task's branch contrast.

Input: the task's highest-yield intents (with what they produced) and its zero-yield intents (dead ends), plus the task goal.

For each meaningful contrast, output a lesson:
- cue: the SITUATION pattern that should trigger the lesson (<=90 chars, e.g. "fuzzing OOXML parsers on Office targets")
- action: the transferable guidance, stated as WHY one shape beat the other (<=200 chars, cite the concrete evidence, e.g. "structure-aware seed mining of worksheet XML beat blind byte-flip mutation 5:0 — build seeds from real documents first")

Rules: only lessons grounded in the given contrast (no generic advice); 0-3 lessons; skip if no clear contrast exists.

Output STRICT JSON array only: [{"cue":"...","action":"..."}]`

// extractContrastiveLessons mines yield-contrast lessons from a completed task.
// Best-effort: never fails the caller.
func (s *Server) extractContrastiveLessons(taskID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// top-3 yielding + bottom-3 dead intents (completed, by child fact/finding count)
	rows, err := s.m.pg.QueryContext(ctx, `
		WITH yields AS (
			SELECT n.id, COALESCE(n.payload->>'summary', n.payload->>'text', '') AS summary,
			       (SELECT count(*) FROM exploration_edges e JOIN exploration_nodes c ON c.id=e.dst_id
			         WHERE e.exploration_id=n.exploration_id AND e.src_id=n.id AND c.kind IN ('fact','finding')) AS y,
			       (SELECT count(*) FROM exploration_edges e JOIN exploration_nodes c ON c.id=e.dst_id
			         WHERE e.exploration_id=n.exploration_id AND e.src_id=n.id AND c.kind='finding') AS f
			FROM exploration_nodes n
			WHERE n.exploration_id=$1 AND n.kind='intent' AND n.state IN ('done','exhausted')
		)
		SELECT (SELECT jsonb_agg(row_to_json(t)) FROM (SELECT * FROM yields WHERE y>0 ORDER BY f DESC, y DESC LIMIT 3) t) AS top,
		       (SELECT jsonb_agg(row_to_json(t)) FROM (SELECT * FROM yields WHERE y=0 ORDER BY id DESC LIMIT 3) t) AS dead`, taskID)
	if err != nil {
		return
	}
	var topJSON, deadJSON []byte
	if rows.Next() {
		if rows.Scan(&topJSON, &deadJSON) != nil {
			rows.Close()
			return
		}
	}
	rows.Close()
	if len(topJSON) == 0 || string(topJSON) == "null" {
		return // nothing yielded: no contrast to learn from
	}
	var goal string
	_ = s.m.pg.QueryRowContext(ctx, `SELECT COALESCE(goal,'') FROM tasks WHERE id=$1`, taskID).Scan(&goal)
	var b strings.Builder
	fmt.Fprintf(&b, "Task goal: %s\n\nHIGH-YIELD intents (winners):\n%s\n\nZERO-YIELD intents (dead ends):\n%s\n",
		clipStr(goal, 300), clipStr(string(topJSON), 2400), clipStr(string(deadJSON), 1200))
	out, err := s.oneShotLLM(ctx, contrastiveSystem, b.String(), 4000)
	if err != nil || strings.TrimSpace(out) == "" {
		reason := "empty output"
		if err != nil {
			reason = err.Error()
		}
		log.Printf("[rsi] contrastive lessons LLM unusable for task %d: %s", taskID, reason)
		return
	}
	var lessons []struct {
		Cue    string `json:"cue"`
		Action string `json:"action"`
	}
	if err := json.Unmarshal([]byte(stripFence(strings.TrimSpace(out))), &lessons); err != nil {
		log.Printf("[rsi] contrastive lessons unparseable for task %d: %v; head=%.200s", taskID, err, out)
		return
	}
	if len(lessons) == 0 {
		log.Printf("[rsi] contrastive lessons: model returned none for task %d", taskID)
		return
	}
	n := 0
	for _, l := range lessons {
		cue, act := strings.TrimSpace(l.Cue), strings.TrimSpace(l.Action)
		if cue == "" || act == "" {
			continue
		}
		key := "contrast:" + slug6(cue)
		if _, err := s.m.pg.ExecContext(ctx, `
			INSERT INTO rsi_lessons(dedupe_key, kind, cue, action, status)
			VALUES ($1,'positive',$2,$3,'candidate')
			ON CONFLICT (dedupe_key) DO NOTHING`, key, clipStr(cue, 120), clipStr(act, 300)); err == nil {
			n++
		} else {
			log.Printf("[rsi] contrastive lesson insert failed for task %d: %v", taskID, err)
		}
	}
	if n > 0 {
		log.Printf("[rsi] task %d: %d contrastive lesson(s) extracted", taskID, n)
	}
}

// updateLessonOutcomes applies the task's result to every lesson it was shown.
func (s *Server) updateLessonOutcomes(taskID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var findings int
	if err := s.m.pg.QueryRowContext(ctx,
		`SELECT count(*) FROM findings WHERE task_id=$1 AND status NOT IN ('false_positive','duplicate')`, taskID).Scan(&findings); err != nil {
		return
	}
	// lessons this task saw (any mode — shadow counts as exposure too)
	rows, err := s.m.pg.QueryContext(ctx,
		`SELECT lesson_id FROM rsi_lesson_usage WHERE task_id=$1`, taskID)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if findings > 0 {
			// promoted: seen + the task produced real findings
			_, _ = s.m.pg.ExecContext(ctx,
				`UPDATE rsi_lessons SET status='adopted' WHERE id=$1 AND status='candidate'`, id)
		} else {
			// strike: zero-finding task saw it; retire at 3 strikes
			_, _ = s.m.pg.ExecContext(ctx, `
				UPDATE rsi_lessons SET retire_strikes = COALESCE(retire_strikes,0)+1 WHERE id=$1`, id)
			var strikes int
			if s.m.pg.QueryRowContext(ctx, `SELECT COALESCE(retire_strikes,0) FROM rsi_lessons WHERE id=$1`, id).Scan(&strikes) == nil && strikes >= 3 {
				_, _ = s.m.pg.ExecContext(ctx, `UPDATE rsi_lessons SET status='retired' WHERE id=$1`, id)
				log.Printf("[rsi] lesson %d retired after %d zero-finding exposures", id, strikes)
			}
		}
	}
}

var rubricLine = regexp.MustCompile(`(?im)^RUBRIC:?\s*(.+)$`)

// retestRubricStats reports judge quality: agreement between the retester's
// latest verdicts and the operator's subsequent finding-status flips. The
// Self-Taught Evaluators measurement — before improving the judge, see it.
func (s *Server) retestRubricStats(w http.ResponseWriter, r *http.Request) {
	rows, err := s.m.pg.QueryContext(r.Context(), `
		SELECT f.id, f.status, rt.verdict, rt.created_at,
		       (rt.evidence ~* 'RUBRIC:') AS has_rubric
		FROM findings f
		JOIN LATERAL (SELECT verdict, evidence, created_at FROM finding_retests
		              WHERE finding_id=f.id ORDER BY id DESC LIMIT 1) rt ON true
		WHERE f.status IN ('confirmed','fixed','false_positive')
		ORDER BY rt.created_at DESC LIMIT 100`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type row struct {
		Finding   int64  `json:"finding_id"`
		Status    string `json:"operator_status"`
		Verdict   string `json:"retest_verdict"`
		Agree     bool   `json:"agree"`
		HasRubric bool   `json:"has_rubric"`
	}
	var out []row
	agree, total, withRubric, rubricAgree := 0, 0, 0, 0
	for rows.Next() {
		var f row
		if rows.Scan(&f.Finding, &f.Status, &f.Verdict, new(time.Time), &f.HasRubric) != nil {
			continue
		}
		f.Agree = verdictAgrees(f.Verdict, f.Status)
		total++
		if f.Agree {
			agree++
		}
		if f.HasRubric {
			withRubric++
			if f.Agree {
				rubricAgree++
			}
		}
		out = append(out, f)
	}
	writeJSON(w, 200, map[string]any{
		"total": total, "agree": agree,
		"agreement_rate":        rate(agree, total),
		"with_rubric":           withRubric,
		"rubric_agreement_rate": rate(rubricAgree, withRubric),
		"rows":                  out,
	})
}

func verdictAgrees(verdict, operator string) bool {
	v, o := strings.ToLower(verdict), strings.ToLower(operator)
	switch {
	case strings.Contains(v, "fixed"):
		return o == "fixed"
	case strings.Contains(v, "confirm") && !strings.Contains(v, "not"):
		return o == "confirmed"
	case strings.Contains(v, "false") || strings.Contains(v, "regress"):
		return o == "false_positive"
	default:
		return false
	}
}

func rate(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func slug6(s string) string {
	var out []rune
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out = append(out, r)
		}
		if len(out) >= 6 {
			break
		}
	}
	if len(out) == 0 {
		return "x"
	}
	return string(out)
}

// onTaskClosedSelfImprove is the Manager's self-improve hook target.
func (s *Server) onTaskClosedSelfImprove(taskID int64) {
	s.extractContrastiveLessons(taskID)
	s.updateLessonOutcomes(taskID)
}

// contrastiveLessonsTrigger: manual backfill endpoint (mirrors the manual
// extractor trigger): POST /api/tasks/{id}/lessons/contrastive.
func (s *Server) contrastiveLessonsTrigger(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid task id")
		return
	}
	s.extractContrastiveLessons(id)
	s.updateLessonOutcomes(id)
	writeJSON(w, 200, map[string]any{"ok": true})
}
