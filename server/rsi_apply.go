package server

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/TaxMachine/ARTEX/rsi"
)

// rsiPolicyApplier: Phase-3 deployment. The adopted Dream-RSI policy biases
// which open intents workers claim first, by rewriting intent priorities
// according to the policy's ordering rule. Scope is deliberately narrow:
//   - only priority values on OPEN intents (never state, never ownership)
//   - only the ordering rules the auditable DSL supports
//   - disabled unless an adopted policy exists (planner unchanged otherwise)
//
// This is the ARTEX translation of Dream-RSI's "improved exploration policy
// redeployed online": our planner LLM stays the base agent; the policy shapes
// the frontier it and the workers see.

const rsiApplyInterval = 5 * time.Minute

func (s *Server) runRsiApplier(ctx context.Context) {
	t := time.NewTicker(rsiApplyInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.applyRsiPolicy(ctx)
		}
	}
}

// activePolicyCode returns the latest adopted policy's code ("" = none).
func (s *Server) activePolicyCode(ctx context.Context) string {
	var code string
	if err := s.m.pg.QueryRowContext(ctx,
		`SELECT code FROM rsi_policies ORDER BY version DESC LIMIT 1`).Scan(&code); err != nil {
		return ""
	}
	return code
}

// applyRsiPolicy rewrites open-intent priorities for every active task per the
// adopted policy's ordering rule.
func (s *Server) applyRsiPolicy(ctx context.Context) {
	code := s.activePolicyCode(ctx)
	if code == "" || code == "seed" {
		return // no adopted policy or explicitly the recorded behavior
	}
	// active tasks
	for _, t := range s.m.List() {
		if t == nil || isTerminalStatus(t.lifecycleSnapshot().Status) {
			continue
		}
		s.applyToTask(ctx, t, code)
	}
}

func (s *Server) applyToTask(ctx context.Context, t *Task, code string) {
	// open intents with their derived-value signal (child facts/findings count)
	rows, err := s.m.pg.QueryContext(ctx, `
		SELECT n.id,
		       (SELECT count(*) FROM exploration_edges e JOIN exploration_nodes c ON c.id = e.dst
		         WHERE e.exploration_id = n.exploration_id AND e.src = n.id AND c.kind IN ('fact','finding')) AS yield,
		       (SELECT count(*) FROM exploration_edges e JOIN exploration_nodes c ON c.id = e.dst
		         WHERE e.exploration_id = n.exploration_id AND e.src = n.id AND c.kind = 'finding') AS findings
		FROM exploration_nodes n
		WHERE n.exploration_id = $1 AND n.kind = 'intent' AND n.state = 'open'`, t.ID)
	if err != nil {
		return
	}
	type row struct {
		id       int64
		yield    int
		findings int
	}
	var list []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.yield, &r.findings) == nil {
			list = append(list, r)
		}
	}
	rows.Close()
	if len(list) < 2 {
		return
	}
	// policy ordering -> priority. Higher priority = claimed first.
	// seed: keep as-is. greedy-K: yield-descending. depth-K: oldest-first (dive).
	type keyed struct {
		id  int64
		key float64
	}
	var keyed_ []keyed
	switch {
	case strings.HasPrefix(code, "greedy-"):
		for _, r := range list {
			keyed_ = append(keyed_, keyed{r.id, float64(r.yield*10 + r.findings*100)})
		}
	case strings.HasPrefix(code, "depth-"):
		for i, r := range list {
			keyed_ = append(keyed_, keyed{r.id, float64(-i)}) // oldest gets highest priority
		}
	default:
		return // rho-greedy affects stopping, not ordering: no priority rewrite
	}
	// stable sort desc by key
	for i := 1; i < len(keyed_); i++ {
		for j := i; j > 0 && keyed_[j].key > keyed_[j-1].key; j-- {
			keyed_[j], keyed_[j-1] = keyed_[j-1], keyed_[j]
		}
	}
	for rank, k := range keyed_ {
		pri := 10 - rank // 10 = first claimed
		if pri < 0 {
			pri = 0
		}
		if _, err := s.m.pg.ExecContext(ctx,
			`UPDATE exploration_nodes SET priority=$2 WHERE id=$1 AND state='open'`, k.id, pri); err != nil {
			log.Printf("[rsi] task %s priority rewrite failed: %v", t.ID, err)
			return
		}
	}
	log.Printf("[rsi] task %s: applied policy %q to %d open intents", t.ID, code, len(keyed_))
}

var _ = rsi.SeedPolicy{}
var _ = strconv.Itoa
