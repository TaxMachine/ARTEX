package rsi

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Dreamer: the policy-development agent. Given the replay trajectories and
// scores of the current policy (and earlier revisions' feedback), pick the
// next policy revision. The paper uses an LLM to rewrite policy CODE; here the
// policy space is a fixed auditable DSL (seed / rho-greedy / greedy-K /
// depth-K), so the "rewrite" is a constrained choice + K. This keeps the loop
// safe (no arbitrary code execution) while preserving the RSI structure:
// propose -> replay-evaluate -> keep-if-better, with the current policy always
// a candidate (monotone by construction).

// Candidate is one proposed policy version.
type Candidate struct {
	Code string
	Note string
}

// Propose runs one dreaming step: evaluate the candidate set over all worlds
// and return the best. If none beats the incumbent, the incumbent is kept.
func (s *Store) Propose(ctx context.Context, incumbent int, lambda float64) (best Candidate, bestV float64, results []map[string]any, err error) {
	return s.ProposeWorlds(ctx, incumbent, lambda, nil)
}

// ProposeWorlds is Propose scoped to specific worlds (nil/empty = all). The
// incumbent's evaluation is scoped the same way, so keep-vs-replace compares
// like with like.
func (s *Store) ProposeWorlds(ctx context.Context, incumbent int, lambda float64, worldIDs []int64) (best Candidate, bestV float64, results []map[string]any, err error) {
	code, _, _, err := s.GetPolicy(ctx, incumbent)
	if err != nil {
		return Candidate{}, 0, nil, fmt.Errorf("incumbent policy v%d: %w", incumbent, err)
	}
	incumbentPolicy, err := CompilePolicy(code)
	if err != nil {
		return Candidate{}, 0, nil, err
	}
	bestV, err = s.EvaluatePolicyWorlds(ctx, incumbent, incumbentPolicy, lambda, worldIDs)
	if err != nil {
		return Candidate{}, 0, nil, err
	}
	best = Candidate{Code: code, Note: "incumbent (kept)"}
	results = append(results, map[string]any{"policy": code, "version": incumbent, "avg_score": bestV, "kept": true})

	// Candidate set: the DSL neighborhood. The paper's LLM would write new
	// code; our DSL is small enough to enumerate, which is strictly more
	// thorough than one LLM proposal per step.
	var candidates []Candidate
	for _, k := range []int{1, 2, 3, 4} {
		candidates = append(candidates, Candidate{Code: fmt.Sprintf("greedy-%d", k), Note: "score-weighted frontier, K=" + fmt.Sprint(k)})
	}
	for _, k := range []int{2, 3} {
		candidates = append(candidates, Candidate{Code: fmt.Sprintf("depth-%d", k), Note: "deep-dive frontier, K=" + fmt.Sprint(k)})
	}
	candidates = append(candidates, Candidate{Code: "rho-greedy", Note: "budget-aware full-frontier"})

	for _, c := range candidates {
		p, cerr := CompilePolicy(c.Code)
		if cerr != nil {
			continue
		}
		// evaluate as a scratch version (negative = not persisted as a policy row)
		v, eerr := s.EvaluatePolicyWorlds(ctx, -1, p, lambda, worldIDs)
		if eerr != nil {
			continue
		}
		results = append(results, map[string]any{"policy": c.Code, "avg_score": v})
		if v > bestV {
			bestV = v
			best = c
		}
	}
	return best, bestV, results, nil
}

// Adopt persists the chosen candidate as the next policy version and returns it.
func (s *Store) Adopt(ctx context.Context, c Candidate, version int, avg float64) error {
	return s.AdoptWorlds(ctx, c, version, avg, nil)
}

// AdoptWorlds is Adopt recording the replay scope (nil/empty = all worlds).
func (s *Store) AdoptWorlds(ctx context.Context, c Candidate, version int, avg float64, worldIDs []int64) error {
	origin := "dreamed"
	if strings.Contains(c.Note, "incumbent") {
		origin = "seed"
	}
	return s.SavePolicyWorlds(ctx, version, c.Code, c.Note, origin, avg, worldIDs)
}

// TrajectoryReport renders the replay trajectories for the operator (the
// "what did dreaming see" view). Scoped to the worlds the version was
// evaluated on (its recorded world_ids; all worlds when unscoped).
func (s *Store) TrajectoryReport(ctx context.Context, version int) ([]map[string]any, error) {
	rows, err := s.pg.QueryContext(ctx, `
		SELECT e.world_id, e.rho, e.calls, e.rounds, e.best_score, e.score
		FROM rsi_evaluations e
		LEFT JOIN rsi_policies p ON p.version = e.policy_version
		WHERE e.policy_version = $1
		  AND (p.version IS NULL OR cardinality(p.world_ids) = 0 OR e.world_id = ANY(p.world_ids))
		ORDER BY e.world_id, e.rho`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var wid int64
		var rho, best, score float64
		var calls, rounds int
		if rows.Scan(&wid, &rho, &calls, &rounds, &best, &score) == nil {
			out = append(out, map[string]any{"world": wid, "rho": rho, "calls": calls, "rounds": rounds, "best_score": best, "V": score})
		}
	}
	return out, rows.Err()
}

// Ranking lists policy versions by average score.
func (s *Store) Ranking(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.pg.QueryContext(ctx, `SELECT version, code, note, origin, COALESCE(avg_score,0), COALESCE(world_ids::text,'') FROM rsi_policies ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var v int
		var code, note, origin string
		var avg float64
		var worldIDsRaw string
		if err := rows.Scan(&v, &code, &note, &origin, &avg, &worldIDsRaw); err != nil {
			return nil, err
		}
		worldIDs := parseInt64Array(worldIDsRaw)
		out = append(out, map[string]any{"version": v, "code": code, "note": note, "origin": origin, "avg_score": avg, "world_ids": worldIDs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["version"].(int) < out[j]["version"].(int) })
	return out, rows.Err()
}
