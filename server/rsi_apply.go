package server

import (
	"context"
	"fmt"
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
// adopted policy's ordering rule, and runs the budget gate (stopping semantics)
// for budget-aware policies.
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
		s.budgetGate(ctx, t, code)
	}
}

// budgetGate implements the adopted policy's STOPPING semantics — the half of
// rho-greedy that made it win the dream replay (recorded planner V=0.0033 vs
// 0.3749): budget-aware exploration stops when the marginal-yield curve
// flattens instead of expanding until exhaustion. Live translation: compare
// discovery yield (new facts/findings) across the last vs previous window of
// completed intents; when it flattens, inject ONE planner hint to stop
// spawning intents and drain the frontier. Advisory by design — the planner
// LLM keeps judgment; the operator sees the gate's numbers in the hint text.
func (s *Server) budgetGate(ctx context.Context, t *Task, code string) {
	if code != "rho-greedy" && !strings.HasPrefix(code, "greedy-") {
		return // stopping semantics only for budget-aware families
	}
	const window = 10 // completed intents per yield window
	// completed intents, oldest first
	rows, err := s.m.pg.QueryContext(ctx, `
		SELECT id, completed_at FROM exploration_nodes
		WHERE exploration_id = $1 AND kind='intent' AND state IN ('done','exhausted','deleted') AND completed_at IS NOT NULL
		ORDER BY completed_at`, t.ID)
	if err != nil {
		return
	}
	type done struct {
		id int64
		at time.Time
	}
	var list []done
	for rows.Next() {
		var d done
		if rows.Scan(&d.id, &d.at) == nil {
			list = append(list, d)
		}
	}
	rows.Close()
	if len(list) < window*2+8 {
		return // not enough history: never gate early work (28-intent floor)
	}
	yieldBetween := func(from, to time.Time) int {
		var n int
		_ = s.m.pg.QueryRowContext(ctx, `
			SELECT count(*) FROM exploration_nodes
			WHERE exploration_id=$1 AND kind IN ('fact','finding') AND created_at > $2 AND created_at <= $3`,
			t.ID, from, to).Scan(&n)
		return n
	}
	n := len(list)
	priorStart := list[n-2*window].at
	mid := list[n-window].at
	last := list[n-1].at
	prior := yieldBetween(priorStart, mid)
	recent := yieldBetween(mid, last)
	// flattening: recent window yields <=20% of prior AND under the absolute
	// floor (3). Both windows have equal intent counts by construction.
	if prior == 0 || recent > prior/5 || recent >= 3 {
		return
	}
	// dedup: one gate hint per task per policy version
	var exists bool
	if err := s.m.pg.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM exploration_nodes
		WHERE exploration_id=$1 AND kind='hint' AND origin='rsi-budget-gate')`, t.ID).Scan(&exists); err != nil || exists {
		return
	}
	text := fmt.Sprintf(
		"[RSI budget gate — policy %s] Marginal discovery yield has flattened: the last %d completed intents produced %d new facts/findings vs %d in the %d before them. "+
			"Per the adopted exploration policy (replay-measured: the recorded expand-everything behavior cost ~100x more per unit of discovery value on tasks 9/10/11): "+
			"do NOT create new intents; let the running intents finish, record their write-backs, and wrap the task up. If a genuinely new lead appears, it can justify breaking this guidance — say so explicitly.",
		code, window, recent, prior, window)
	if _, err := t.Store.AddNode("hint", map[string]any{"text": text}, 0, "active", "rsi-budget-gate", nil); err != nil {
		log.Printf("[rsi] task %s budget gate hint failed: %v", t.ID, err)
		return
	}
	log.Printf("[rsi] task %s budget gate FIRED: %d completed intents, yield %d -> %d (windows of %d)", t.ID, n, prior, recent, window)
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
