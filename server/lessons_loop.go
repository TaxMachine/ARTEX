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

const contrastiveSystem = `You distill exploration lessons for an autonomous pentest platform from ONE completed task's branch contrast — and you separate transferable GUIDANCE from PLATFORM DEFECTS.

Input: the task's highest-yield intents (with what they produced) and its zero-yield intents (dead ends), plus the task goal.

Two output kinds:
1. LESSONS — transferable exploration guidance (injected into future worker prompts):
   - cue: the SITUATION pattern that should trigger it (<=90 chars, e.g. "fuzzing OOXML parsers on Office targets")
   - action: WHY one shape beat the other, evidence-cited (<=200 chars)
2. ISSUES — defects in the PLATFORM ITSELF that the run worked around or suffered from (broken tools, wrong tool behavior, failed integrations — e.g. "ssh_upload always fails", "captures not attributed"). These go to the operator's fix backlog, NOT into worker prompts:
   - title: short imperative summary (<=100 chars, e.g. "ssh_upload fails on Windows targets")
   - detail: what was observed, which intent hit it, what was used as workaround

Rules: only items grounded in the given evidence; a workaround implies an issue (the workers shouldn't need workarounds); 0-3 lessons and 0-3 issues; skip kinds with nothing real.

Output STRICT JSON only: {"lessons":[{"cue":"...","action":"..."}],"issues":[{"title":"...","detail":"..."}]}`

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
	out, err := s.oneShotLLM(ctx, contrastiveSystem, b.String(), 8000)
	if err != nil || strings.TrimSpace(out) == "" {
		reason := "empty output"
		if err != nil {
			reason = err.Error()
		}
		log.Printf("[rsi] contrastive lessons LLM unusable for task %d: %s", taskID, reason)
		return
	}
	var parsed struct {
		Lessons []struct {
			Cue    string `json:"cue"`
			Action string `json:"action"`
		} `json:"lessons"`
		Issues []struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"issues"`
	}
	if jerr := json.Unmarshal([]byte(stripFence(strings.TrimSpace(out))), &parsed); jerr != nil {
		// salvage: reasoning-heavy models occasionally truncate; recover any
		// complete objects from the cut-off arrays before giving up.
		if !salvageContrastive(strings.TrimSpace(out), &parsed) {
			log.Printf("[rsi] contrastive lessons unparseable for task %d: %v; head=%.200s", taskID, jerr, out)
			return
		}
		log.Printf("[rsi] contrastive output was truncated; salvaged %d lesson(s), %d issue(s)", len(parsed.Lessons), len(parsed.Issues))
	}
	if len(parsed.Lessons) == 0 && len(parsed.Issues) == 0 {
		log.Printf("[rsi] contrastive lessons: model returned none for task %d", taskID)
		return
	}
	n := 0
	for _, l := range parsed.Lessons {
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
	for _, iss := range parsed.Issues {
		title, detail := strings.TrimSpace(iss.Title), strings.TrimSpace(iss.Detail)
		if title == "" {
			continue
		}
		s.filePlatformIssue("contrastive", fmt.Sprintf("task:%d", taskID), title, detail)
	}
	log.Printf("[rsi] task %d: %d contrastive lesson(s), %d platform issue(s)", taskID, n, len(parsed.Issues))
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

// salvageContrastive recovers complete {"cue","action"} / {"title","detail"}
// objects from a truncated JSON response by brace-scanning.
func salvageContrastive(raw string, parsed *struct {
	Lessons []struct {
		Cue    string `json:"cue"`
		Action string `json:"action"`
	} `json:"lessons"`
	Issues []struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	} `json:"issues"`
}) bool {
	found := false
	depth := 0
	start := -1
	for i, r := range raw {
		switch r {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					obj := raw[start : i+1]
					var probe map[string]any
					if json.Unmarshal([]byte(obj), &probe) == nil {
						if cue, ok := probe["cue"].(string); ok {
							act, _ := probe["action"].(string)
							parsed.Lessons = append(parsed.Lessons, struct {
								Cue    string `json:"cue"`
								Action string `json:"action"`
							}{cue, act})
							found = true
						} else if title, ok := probe["title"].(string); ok {
							det, _ := probe["detail"].(string)
							parsed.Issues = append(parsed.Issues, struct {
								Title  string `json:"title"`
								Detail string `json:"detail"`
							}{title, det})
							found = true
						}
					}
					start = -1
				}
			}
		}
	}
	return found
}

// slugify renders an archive-safe name fragment for world exports.
func slugify(s string) string {
	var out []rune
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			if len(out) > 0 && out[len(out)-1] != '-' {
				out = append(out, '-')
			}
		}
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return "task"
	}
	return string(out)
}
