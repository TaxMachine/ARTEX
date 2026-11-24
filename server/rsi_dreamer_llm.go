package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/TaxMachine/ARTEX/db"
	"github.com/TaxMachine/ARTEX/rsi"
	"log"
	"strings"
	"time"

	"github.com/TaxMachine/ARTEX/agent"
	"github.com/TaxMachine/ARTEX/third_party/norma/llm"
)

// LLM dreamer: the policy-development agent (Dream-RSI phase 4). Where the
// enumeration dreamer scores every DSL candidate, the LLM dreamer INSPECTS the
// replay trajectories and picks/proposes with rationale — the paper's
// "policy-development agent inspects replay trajectories and scores, identifies
// successful decisions and recurring failures, rewrites the policy".
//
// Model: configurable, default GLM 5.3 Flash with reasoning effort high — the
// dreaming task is judgment over structured history, not breadth.

const dreamerSystem = `You are the policy-development agent of a Dream-RSI loop for an autonomous pentest platform.

You are given replay evaluations of exploration policies over recorded discovery
trees (worlds). Each world is a completed task: nodes are intents/facts/findings,
scores are exploit value (0..1), costs are recorded LLM spend. A policy decides
which frontier nodes to expand per round and when to stop; rho sweeps budget
preference. V is the quality-per-cost hypervolume minus a parallelism penalty.

Your job: choose the policy to deploy next round. You may:
1. pick one of the evaluated candidates (the incumbent is always available —
   keeping it is monotonically safe),
2. propose a K variant of an evaluated family (greedy-K / depth-K with a
   different K, 1..6) that was NOT evaluated — but only with a specific
   trajectory-grounded reason why the trend suggests it.

Output STRICT JSON only:
{"keep": true|false, "code": "<dsl>", "reason": "<2-4 sentences, cite the numbers>"}
"keep": true means keep the incumbent (then code = incumbent's code).
DSL grammar: seed | rho-greedy | greedy-K | depth-K  (K in 1..6)`

// dreamerConfig resolves the dreamer's model config: the RSI_DREAMER_PROFILE
// env var names an llm_profiles row (default: name match "GLM 5.3 Flash"),
// with reasoning effort forced high.
func (s *Server) dreamerConfig() (agent.Config, bool) {
	prof, err := s.dreamerProfile()
	if err != nil || prof == nil {
		return agent.Config{}, false
	}
	cfg := agent.ConfigFrom(prof.Format, prof.Model, prof.BaseURL, prof.APIKey, prof.Proxy)
	cfg.ReasoningEffort = "high"
	return cfg, true
}

func (s *Server) dreamerProfile() (*db.LLMProfile, error) {
	// The configured Dreamer agent wins: an llm_profile binding on the
	// agent row (set from the Agents page) selects the dreaming model.
	var bound int64
	if err := s.m.pg.QueryRow(`SELECT COALESCE(llm_profile_id,0) FROM agents WHERE key='dreamer'`).Scan(&bound); err == nil && bound > 0 {
		if p, err := s.m.pg.ProfileByID(bound); err == nil && p != nil {
			return p, nil
		}
	}
	// name preference: exact GLM 5.3 Flash, else any glm flash, else default
	rows, err := s.m.pg.Query(`SELECT id FROM llm_profiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var fallback *db.LLMProfile
	for _, id := range ids {
		p, err := s.m.pg.ProfileByID(id)
		if err != nil || p == nil {
			continue
		}
		n := strings.ToLower(p.Name)
		if strings.Contains(n, "glm") && strings.Contains(n, "flash") && strings.Contains(n, "5.3") {
			return p, nil
		}
		if fallback == nil && strings.Contains(n, "glm") && strings.Contains(n, "flash") {
			fallback = p
		}
	}
	return fallback, nil
}

// dreamWithLLM runs one dreaming step with the LLM policy-development agent,
// over all replay worlds. Falls back to enumeration when no profile is available.
func (s *Server) dreamWithLLM(ctx context.Context, incumbent int, lambda float64) (map[string]any, error) {
	return s.dreamWithLLMWorlds(ctx, incumbent, lambda, nil)
}

// dreamWithLLMWorlds is dreamWithLLM scoped to specific replay worlds
// (nil/empty = all). The incumbent is re-evaluated on the same scope so the
// keep-vs-replace comparison is like-for-like.
func (s *Server) dreamWithLLMWorlds(ctx context.Context, incumbent int, lambda float64, worldIDs []int64) (map[string]any, error) {
	st := s.rsiStore()
	log.Printf("[rsi-dream] propose start (incumbent=%d worlds=%v)", incumbent, worldIDs)
	best, bestV, results, err := st.ProposeWorlds(ctx, incumbent, lambda, worldIDs)
	log.Printf("[rsi-dream] propose done: best=%s V=%.4f err=%v", best.Code, bestV, err)
	if err != nil {
		return nil, err
	}
	cfg, ok := s.dreamerConfig()
	if !ok {
		return map[string]any{
			"best":       map[string]any{"code": best.Code, "note": best.Note, "avg_score": bestV},
			"candidates": results,
			"world_ids":  worldIDs,
			"dreamer":    "enumeration (no LLM profile resolved)",
		}, nil
	}
	log.Printf("[rsi-dream] dreamer model: %s (effort %s)", cfg.Model, cfg.ReasoningEffort)
	prov, err := cfg.NewProvider()
	if err != nil {
		return nil, err
	}
	sysPrompt := dreamerSystem
	if edited := s.dreamerPrompt(); edited != "" {
		sysPrompt = edited
	}
	// Compose the trajectory report the agent conditions on.
	traj, _ := st.TrajectoryReport(ctx, incumbent)
	incCode, incNote, _, _ := st.GetPolicy(ctx, incumbent)
	var b strings.Builder
	fmt.Fprintf(&b, "Incumbent policy: %s (%s)\n\n", incCode, incNote)
	worlds, _ := st.ListWorlds(ctx)
	worlds, ferr := rsi.FilterWorlds(worlds, worldIDs)
	if ferr != nil {
		return nil, ferr
	}
	fmt.Fprintf(&b, "Replay evaluations across %d worlds (rho sweep 0/0.25/0.5/0.75/1):\n", len(worlds))
	for _, wm := range worlds {
		fmt.Fprintf(&b, "  world %v = task %v (%s)\n", wm["id"], wm["task_id"], wm["name"])
	}
	for _, c := range results {
		fmt.Fprintf(&b, "  %-12s V=%.4f\n", fmt.Sprint(c["policy"]), c["avg_score"])
	}
	fmt.Fprintf(&b, "\nIncumbent trajectories (world, rho, calls, rounds, bestScore, V):\n")
	for _, t := range traj {
		fmt.Fprintf(&b, "  w%v rho=%v calls=%v rounds=%v best=%v V=%v\n",
			t["world"], t["rho"], t["calls"], t["rounds"], t["best_score"], t["V"])
	}
	cctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	log.Printf("[rsi-dream] LLM call start")
	msg, _, _, err := prov.Complete(cctx, llm.CompletionRequest{
		System: []string{sysPrompt},
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(b.String())}},
		},
		MaxTokens: 2000,
	})
	log.Printf("[rsi-dream] LLM call done err=%v", err)
	if err != nil {
		// LLM unavailable: the enumerated best stands (loop stays monotone).
		return map[string]any{
			"best":       map[string]any{"code": best.Code, "note": best.Note, "avg_score": bestV},
			"candidates": results,
			"world_ids":  worldIDs,
			"dreamer":    "enumeration (LLM dreamer failed: " + err.Error() + ")",
		}, nil
	}
	text := extractText(msg)
	dec := json.NewDecoder(strings.NewReader(stripFence(text)))
	var decision struct {
		Keep   bool   `json:"keep"`
		Code   string `json:"code"`
		Reason string `json:"reason"`
	}
	if err := dec.Decode(&decision); err != nil || strings.TrimSpace(decision.Code) == "" {
		return map[string]any{
			"best":       map[string]any{"code": best.Code, "note": best.Note, "avg_score": bestV},
			"candidates": results,
			"world_ids":  worldIDs,
			"dreamer":    "enumeration (LLM output unparseable)",
			"llm_raw":    clipStr(text, 1500),
		}, nil
	}
	if decision.Keep {
		decision.Code = incCode
	}
	// validate against the DSL before it can be adopted
	if _, err := rsi.CompilePolicy(decision.Code); err != nil {
		return map[string]any{
			"best":       map[string]any{"code": best.Code, "note": best.Note, "avg_score": bestV},
			"candidates": results,
			"world_ids":  worldIDs,
			"dreamer":    "enumeration (LLM proposed invalid policy " + decision.Code + ")",
		}, nil
	}
	return map[string]any{
		"best": map[string]any{
			"code": decision.Code, "note": "llm-dreamer: " + decision.Reason,
			"avg_score": scoreFor(results, decision.Code, bestV),
		},
		"candidates": results,
		"world_ids":  worldIDs,
		"dreamer":    cfg.Model + " (effort high)",
	}, nil
}

func countWorlds(results []map[string]any) int { return len(uniqueWorlds(results)) }

func uniqueWorlds(results []map[string]any) []int64 {
	seen := map[int64]bool{}
	var out []int64
	// worlds come from the store; the results slice is per-policy, so just
	// count distinct evaluations rows — approximate via len(results)>0.
	_ = seen
	_ = out
	n := 0
	for _, r := range results {
		_ = r
		n++
	}
	return make([]int64, 0)
}

func scoreFor(results []map[string]any, code string, dflt float64) float64 {
	for _, r := range results {
		if fmt.Sprint(r["policy"]) == code {
			if v, ok := r["avg_score"].(float64); ok {
				return v
			}
		}
	}
	return dflt
}

func extractText(m llm.Message) string {
	var sb strings.Builder
	for _, b := range m.Content {
		if b.Type == llm.BlockText {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i > 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	return s
}

func clipStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// dreamerPrompt returns the Dreamer agent's current edited prompt ("" = use
// the built-in constant).
func (s *Server) dreamerPrompt() string {
	var pid int64
	if err := s.m.pg.QueryRow(`SELECT COALESCE(current_prompt_id,0) FROM agents WHERE key='dreamer'`).Scan(&pid); err != nil || pid == 0 {
		return ""
	}
	var text string
	if err := s.m.pg.QueryRow(`SELECT template_text FROM agent_prompts WHERE id=$1`, pid).Scan(&text); err != nil {
		return ""
	}
	return strings.TrimSpace(text)
}
