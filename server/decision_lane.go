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
	"sync"
	"time"

	"github.com/TaxMachine/ARTEX/db"
	"github.com/TaxMachine/ARTEX/llmrec"
)

// Decision lane: small, high-volume adjudication calls factored out of the
// generation roles. A decision model (a profile bound to the 'decision' agent)
// answers fixed-enum questions — is this severity honest, does this retest
// verdict follow from the evidence, is this finding a duplicate — while the
// big models keep generating. Every decision is recorded with the model's
// answer next to the agent's claim; disagreements are surfaced as task
// activity, never silently applied: the agent that ran the work owns its
// record, the decision model provides independent review.

// Lenient extraction patterns for truncated decision output (see
// oneShotDecision): the leading "decision" field survives truncation because
// the schema puts it first.
var (
	decisionFieldRe  = regexp.MustCompile(`"decision"\s*:\s*"([^"]+)`)
	decisionConfRe   = regexp.MustCompile(`"confidence"\s*:\s*([0-9.]+)`)
	decisionReasonRe = regexp.MustCompile(`"reason"\s*:\s*"([^"]*)`)
)

// decisionWorkerLane tags llm_usage rows from this lane.
const decisionWorkerLane = "decision"

// DecisionScopes enumerates the adjudication types.
const (
	DecisionScopeFinding = "finding_adjudication" // severity + vulnclass vs evidence
	DecisionScopeRetest  = "retest_verdict"       // verdict vs retest evidence
	DecisionScopeDedupe  = "finding_dedupe"       // new finding vs project candidates
)

// DecisionAgentKey is the seeded agent row that owns the model binding.
const DecisionAgentKey = "decision"

// DecisionDefaultPrompt seeds the decision agent. Editable on the Agents page;
// read per call.
const DecisionDefaultPrompt = `You are the adjudicator of an autonomous pentest platform. You answer fixed-choice questions about vulnerability records with a single decision — no hedging, no essays.

You will get: the worker's claim (severity / exploit class / verdict / duplicate suspicion), the record's evidence, and the closed option set. Judge ONLY from what the evidence actually shows: prefer the conservative option when impact is asserted but not demonstrated. A claimed-but-unproven critical is a medium; an exploit class named without the matching primitive in the anatomy is the nearest class that IS shown; a retest that observed the key behavior is reproduced, one that hit environment blockers is inconclusive.

Output STRICT JSON only: {"decision":"<one of the offered options>","confidence":0..1,"reason":"<one sentence>"}`

// decisionProfile resolves the decision model's profile: the agent binding
// wins, else the active/default profile (the lane then rides the same model
// until an operator binds a dedicated one).
func (s *Server) decisionProfile() (*db.LLMProfile, error) {
	var bound int64
	if err := s.m.pg.QueryRow(`SELECT COALESCE(llm_profile_id,0) FROM agents WHERE key=$1`, DecisionAgentKey).Scan(&bound); err == nil && bound > 0 {
		if p, err := s.m.pg.ProfileByID(bound); err == nil && p != nil {
			return p, nil
		}
	}
	return s.m.pg.ActiveProfile()
}

// oneShotDecision runs one enum decision. options is the closed set; the
// caller supplies the question text (already compact). Returns the parsed
// decision; on any failure the error is the caller's to log and skip — a
// decision-lane failure must never block the record it reviews.
func (s *Server) oneShotDecision(ctx context.Context, question string, options []string, maxChars int) (decision string, confidence float64, reason string, profileID int64, err error) {
	prof, err := s.decisionProfile()
	if err != nil || prof == nil {
		return "", 0, "", 0, fmt.Errorf("no decision model profile: %w", err)
	}
	prov, _, ok := s.providerForProfile(prof.ID)
	if !ok {
		return "", 0, "", 0, fmt.Errorf("decision model profile %d unavailable", prof.ID)
	}
	sys := DecisionDefaultPrompt
	if edited := s.decisionPrompt(); edited != "" {
		sys = edited
	}
	if maxChars <= 0 || maxChars > 40000 {
		maxChars = 12000
	}
	if len(question) > maxChars {
		question = question[:maxChars]
	}
	qctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ctx = llmrec.WithWorker(qctx, decisionWorkerLane)
	// One retry: a first attempt that returns empty or unparseable output (a
	// reasoning-heavy model can spend the whole budget thinking) is cheap to
	// redo; a second identical failure propagates.
	text, err := streamCollectText(ctx, prov, sys, question, 640)
	if err != nil {
		return "", 0, "", prof.ID, err
	}
	if strings.TrimSpace(stripFence(text)) == "" {
		text, err = streamCollectText(ctx, prov, sys, question, 640)
		if err != nil {
			return "", 0, "", prof.ID, err
		}
	}
	var out struct {
		Decision   string  `json:"decision"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
	}
	raw := strings.TrimSpace(stripFence(text))
	if jerr := json.Unmarshal([]byte(raw), &out); jerr != nil || out.Decision == "" {
		// Lenient fallback: a decision model can run past the token budget
		// mid-reason, leaving truncated JSON. The decision field itself is the
		// contract — extract it rather than discarding a valid verdict.
		if m := decisionFieldRe.FindStringSubmatch(raw); m != nil {
			out.Decision = strings.Trim(m[1], `"' `)
			if m2 := decisionConfRe.FindStringSubmatch(raw); m2 != nil {
				if v, err := strconv.ParseFloat(m2[1], 64); err == nil {
					out.Confidence = v
				}
			}
			// reason may be truncated; take what is there
			if m3 := decisionReasonRe.FindStringSubmatch(raw); m3 != nil {
				out.Reason = m3[1]
			}
		}
		if out.Decision == "" {
			return "", 0, "", prof.ID, fmt.Errorf("decision model output unparseable: %.200s", text)
		}
	}
	ok = false
	for _, o := range options {
		if strings.EqualFold(strings.TrimSpace(out.Decision), o) {
			out.Decision = o
			ok = true
			break
		}
	}
	if !ok {
		return "", 0, "", prof.ID, fmt.Errorf("decision %q not in option set %v", out.Decision, options)
	}
	if out.Confidence < 0 {
		out.Confidence = 0
	}
	if out.Confidence > 1 {
		out.Confidence = 1
	}
	return out.Decision, out.Confidence, out.Reason, prof.ID, nil
}

// decisionLaneDisabled honours the operator kill switch: the setting
// decision_lane_enabled, when explicitly "false", suspends every adjudicator
// (the decision agent row can stay configured for when it is switched back).
func (s *Server) decisionLaneDisabled() bool {
	v, _, _ := s.m.pg.GetSetting("decision_lane_enabled")
	return v == "false"
}

var decisionInflight sync.Map // ref key -> struct{} ; one adjudication per record

func decisionInflightKey(scope string, ref int64) string {
	return fmt.Sprintf("%s:%d", scope, ref)
}

// recordDecision persists one adjudication (best-effort).
func (s *Server) recordDecision(scope string, ref int64, taskID string, question, agentValue, modelDecision, reason string, confidence float64, profileID int64, agree bool) {
	if s.m.pg == nil {
		return
	}
	q := question
	if len(q) > 8000 {
		q = q[:8000]
	}
	r := reason
	if len(r) > 2000 {
		r = r[:2000]
	}
	if _, err := s.m.pg.Exec(`INSERT INTO decision_lane (scope, ref_id, task_id, question, agent_value, model_decision, model_reason, model_confidence, profile_id, agree)
		VALUES ($1,$2,NULLIF($3,''),$4,NULLIF($5,''),$6,NULLIF($7,''),$8,NULLIF($9,0),$10)`,
		scope, ref, taskID, q, agentValue, modelDecision, r, confidence, profileID, agree); err != nil {
		log.Printf("[decision:%s] record failed: %v", scope, err)
	}
}

// emitDecisionActivity surfaces a disagreement on the task's activity stream
// so the planner/operator sees it (best-effort).
func (s *Server) emitDecisionActivity(taskID string, summary string) {
	if taskID == "" || s.m.pg == nil {
		return
	}
	t := s.m.ResolveTask(taskID)
	if t == nil {
		return
	}
	if _, err := s.m.pg.Exploration(t.ExpID).AppendActivity(db.Activity{
		Worker: "system", Kind: "system", Summary: summary,
	}); err != nil {
		log.Printf("[decision] activity emit on task %s failed: %v", taskID, err)
	}
}

// decisionPrompt returns the decision agent's current edited prompt ("" = the
// built-in constant), mirroring the dreamer's lookup.
func (s *Server) decisionPrompt() string {
	var pid int64
	if err := s.m.pg.QueryRow(`SELECT COALESCE(current_prompt_id,0) FROM agents WHERE key=$1`, DecisionAgentKey).Scan(&pid); err != nil || pid == 0 {
		return ""
	}
	var text string
	if err := s.m.pg.QueryRow(`SELECT template_text FROM agent_prompts WHERE id=$1`, pid).Scan(&text); err != nil {
		return ""
	}
	return strings.TrimSpace(text)
}

// goAdjudicate runs fn in the background with the server's long-lived context,
// guarded against duplicate adjudications of the same record.
func (s *Server) goAdjudicate(scope string, ref int64, fn func(ctx context.Context)) {
	key := decisionInflightKey(scope, ref)
	if _, busy := decisionInflight.LoadOrStore(key, struct{}{}); busy {
		return
	}
	go func() {
		defer decisionInflight.Delete(key)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[decision:%s] panic: %v", scope, r)
			}
		}()
		ctx, cancel := context.WithTimeout(s.ctx, 90*time.Second)
		defer cancel()
		fn(ctx)
	}()
}

// DecisionLaneStats aggregates agreement per scope for the UI/stats endpoint.
type DecisionLaneStats struct {
	Scope     string  `json:"scope"`
	Calls     int64   `json:"calls"`
	Agree     int64   `json:"agree"`
	Disagree  int64   `json:"disagree"`
	AgreeRate float64 `json:"agree_rate"`
}

// decisionStats reads the per-scope agreement aggregates.
func (s *Server) decisionStats() []DecisionLaneStats {
	rows, err := s.m.pg.Query(`SELECT scope, count(*), count(*) FILTER (WHERE agree), count(*) FILTER (WHERE NOT agree)
		FROM decision_lane GROUP BY scope ORDER BY scope`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []DecisionLaneStats{}
	for rows.Next() {
		var st DecisionLaneStats
		if rows.Scan(&st.Scope, &st.Calls, &st.Agree, &st.Disagree) == nil {
			if st.Calls > 0 {
				st.AgreeRate = float64(st.Agree) / float64(st.Calls)
			}
			out = append(out, st)
		}
	}
	return out
}

// decisionStatsHandler: GET /api/decision/stats — per-scope agreement
// aggregates for the Agents/config UI.
func (s *Server) decisionStatsHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.decisionStats())
}
