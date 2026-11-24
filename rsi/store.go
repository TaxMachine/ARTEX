package rsi

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/TaxMachine/ARTEX/db"
)

// Store: persistence for worlds/policies/evaluations + the task-export path.

type Store struct{ pg *db.DB }

func NewStore(pg *db.DB) *Store { return &Store{pg: pg} }

// ExportTask builds a replay world from a completed task's exploration graph:
// nodes with evaluator quality + recorded cost, parent links from the
// derivation edges. Idempotent per (task, name).
func (s *Store) ExportTask(ctx context.Context, taskID int64, name string) (int64, error) {
	// nodes + payloads
	rows, err := s.pg.QueryContext(ctx, `
		SELECT id, kind, payload FROM exploration_nodes WHERE exploration_id = $1`, taskID)
	if err != nil {
		return 0, err
	}
	type rawNode struct {
		id      int64
		kind    string
		payload map[string]any
	}
	var raws []rawNode
	for rows.Next() {
		var r rawNode
		var pj []byte
		if err := rows.Scan(&r.id, &r.kind, &pj); err != nil {
			rows.Close()
			return 0, err
		}
		_ = json.Unmarshal(pj, &r.payload)
		raws = append(raws, r)
	}
	rows.Close()
	if len(raws) == 0 {
		return 0, fmt.Errorf("task %d has no exploration nodes", taskID)
	}

	// parent links from the STRUCTURAL edges only: derived_from (results
	// anchor to the intent that produced them) and spawns (planner-created
	// intents chain to their origin). covers/yields/proves are semantic
	// cross-links — treating them as tree parents produced disconnected
	// forests where replay could not reach whole subtrees.
	parent := map[int64]int64{}
	childCount := map[int64]int{}
	erows, err := s.pg.QueryContext(ctx, `
		SELECT dst_id, src_id FROM exploration_edges
		WHERE exploration_id = $1 AND rel IN ('derived_from','spawns')`, taskID)
	if err == nil {
		for erows.Next() {
			var dst, src int64
			if erows.Scan(&dst, &src) == nil {
				if _, seen := parent[dst]; !seen {
					parent[dst] = src
				}
				childCount[src]++
			}
		}
		erows.Close()
	}

	// cost per node: tokens attributed through activity rows
	cost := map[int64]float64{}
	crows, err := s.pg.QueryContext(ctx, `
		SELECT node_id, SUM(input_tokens + output_tokens) FROM activity
		WHERE exploration_id = $1 AND node_id IS NOT NULL GROUP BY node_id`, taskID)
	if err == nil {
		for crows.Next() {
			var id int64
			var tok float64
			if crows.Scan(&id, &tok) == nil {
				cost[id] = tok / 1000.0 // cost units: 1e3 tokens
			}
		}
		crows.Close()
	}

	w := World{Name: name, TaskID: taskID}
	for _, r := range raws {
		q, comp := EvaluateNode(r.kind, r.payload, childCount[r.id], cost[r.id])
		w.Nodes = append(w.Nodes, Node{
			ID: r.id, Kind: r.kind, Parent: parent[r.id],
			Score: q, Cost: cost[r.id], At: 0,
		})
		if r.kind == "goal" {
			// goals are roots in ARTEX's planner graph; use the oldest as root
			w.Root = r.id
		}
		_ = comp
	}
	if w.Root == 0 {
		w.Root = raws[0].id
	}
	// Forest repair: any node with no structural parent hangs off the root —
	// the recorded planner did create intents top-level, so that is faithful.
	for i := range w.Nodes {
		if w.Nodes[i].Parent == 0 && w.Nodes[i].ID != w.Root {
			w.Nodes[i].Parent = w.Root
		}
	}
	w.B = len(w.Nodes)
	for _, n := range w.Nodes {
		if n.Score > w.S {
			w.S = n.Score
		}
		w.CostSum += n.Cost
	}

	tree, _ := json.Marshal(w)
	stats, _ := json.Marshal(map[string]any{"B": w.B, "S": w.S, "cost_sum": w.CostSum})
	var id int64
	err = s.pg.QueryRowContext(ctx, `
		INSERT INTO rsi_worlds (task_id, name, tree, stats)
		VALUES ($1,$2,$3::jsonb,$4::jsonb)
		ON CONFLICT (task_id, name) DO UPDATE SET tree = EXCLUDED.tree, stats = EXCLUDED.stats
		RETURNING id`, taskID, name, string(tree), string(stats)).Scan(&id)
	return id, err
}

// LoadWorld fetches one replay world.
func (s *Store) LoadWorld(ctx context.Context, id int64) (*World, error) {
	var w World
	var tree []byte
	if err := s.pg.QueryRowContext(ctx, `SELECT id, task_id, name, tree FROM rsi_worlds WHERE id=$1`, id).Scan(&w.ID, &w.TaskID, &w.Name, &tree); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(tree, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// ListWorlds returns every stored world id/name.
func (s *Store) ListWorlds(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.pg.QueryContext(ctx, `SELECT id, task_id, name, created_at FROM rsi_worlds ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, task int64
		var name string
		var at time.Time
		if rows.Scan(&id, &task, &name, &at) == nil {
			out = append(out, map[string]any{"id": id, "task_id": task, "name": name, "created_at": at.Format(time.RFC3339)})
		}
	}
	return out, rows.Err()
}

// EvaluatePolicy runs the rho sweep over every stored world and persists the
// per-world objective. Returns the average V^m.
func (s *Store) EvaluatePolicy(ctx context.Context, version int, p Policy, lambda float64) (float64, error) {
	return s.EvaluatePolicyWorlds(ctx, version, p, lambda, nil)
}

// EvaluatePolicyWorlds is EvaluatePolicy scoped to specific worlds. A nil or
// empty filter means all worlds (the historical default). The persisted
// rsi_evaluations rows are scoped the same way so TrajectoryReport matches
// what was evaluated.
func (s *Store) EvaluatePolicyWorlds(ctx context.Context, version int, p Policy, lambda float64, worldIDs []int64) (float64, error) {
	worlds, err := s.ListWorlds(ctx)
	if err != nil {
		return 0, err
	}
	worlds, err = FilterWorlds(worlds, worldIDs)
	if err != nil {
		return 0, err
	}
	total := 0.0
	for wi, wm := range worlds {
		id := wm["id"].(int64)
		w, err := s.LoadWorld(ctx, id)
		if err != nil {
			return 0, err
		}
		_ = wi
		sweep := []float64{0, 0.25, 0.5, 0.75, 1}
		replays := map[float64][]Reveal{}
		for _, rho := range sweep {
			traj := Replay(w, p, rho, 20)
			replays[rho] = traj
			var calls, rounds int
			var best float64
			if len(traj) > 0 {
				last := traj[len(traj)-1]
				calls, rounds, best = last.Calls, last.Rounds, last.Score
			}
			_, err := s.pg.ExecContext(ctx, `
				INSERT INTO rsi_evaluations (policy_version, world_id, rho, calls, rounds, best_score, score)
				VALUES ($1,$2,$3,$4,$5,$6,0)
				ON CONFLICT (policy_version, world_id, rho) DO UPDATE SET
					calls=EXCLUDED.calls, rounds=EXCLUDED.rounds, best_score=EXCLUDED.best_score`,
				version, id, rho, calls, rounds, best)
			if err != nil {
				return 0, err
			}
		}
		v := Objective(replays, w.B, lambda)
		total += v
		// persist the per-world score on the rho=0.5 row (the summary row)
		if _, err := s.pg.ExecContext(ctx, `
			UPDATE rsi_evaluations SET score=$4 WHERE policy_version=$1 AND world_id=$2 AND rho=$3`,
			version, id, 0.5, v); err != nil {
			return 0, err
		}
	}
	return total / float64(len(worlds)), nil
}

// SavePolicy stores a policy version evaluated over all worlds.
func (s *Store) SavePolicy(ctx context.Context, version int, code, note, origin string, avg float64) error {
	return s.SavePolicyWorlds(ctx, version, code, note, origin, avg, nil)
}

// SavePolicyWorlds stores a policy version with its replay scope: the world
// ids it was evaluated on (nil/empty = all worlds).
func (s *Store) SavePolicyWorlds(ctx context.Context, version int, code, note, origin string, avg float64, worldIDs []int64) error {
	if len(worldIDs) == 0 {
		worldIDs = []int64{}
	}
	_, err := s.pg.ExecContext(ctx, `
		INSERT INTO rsi_policies (version, code, note, origin, avg_score, world_ids)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (version) DO UPDATE SET code=EXCLUDED.code, note=EXCLUDED.note,
			origin=EXCLUDED.origin, avg_score=EXCLUDED.avg_score, world_ids=EXCLUDED.world_ids`,
		version, code, note, origin, avg, worldIDs)
	return err
}

// PolicyByName loads a policy by version.
func (s *Store) GetPolicy(ctx context.Context, version int) (code string, note string, avg float64, err error) {
	err = s.pg.QueryRowContext(ctx, `SELECT code, note, COALESCE(avg_score,0) FROM rsi_policies WHERE version=$1`, version).Scan(&code, &note, &avg)
	return
}

// PolicyWorldScope returns the worlds a policy version was evaluated on.
// An empty result means all worlds.
func (s *Store) PolicyWorldScope(ctx context.Context, version int) ([]int64, error) {
	var raw string
	if err := s.pg.QueryRowContext(ctx, `SELECT COALESCE(world_ids::text,'') FROM rsi_policies WHERE version=$1`, version).Scan(&raw); err != nil {
		return nil, err
	}
	return parseInt64Array(raw), nil
}

// parseInt64Array parses PostgreSQL's textual bigint[] form ("{1,2}"). The
// pgx stdlib driver returns arrays in this text form to database/sql callers;
// scanning directly into []int64 fails and silently drops every row.
func parseInt64Array(raw string) []int64 {
	out := []int64{}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "NULL" {
		return out
	}
	raw = strings.TrimPrefix(raw, "{")
	raw = strings.TrimSuffix(raw, "}")
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if v, err := strconv.ParseInt(part, 10, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// CompilePolicy maps stored policy code to a Policy. The DSL is a fixed,
// auditable grammar — NOT arbitrary Go execution. Supported forms:
//
//	seed               expand-all BFS (recorded planner behavior)
//	rho-greedy         frontier expansion gated on rho
//	greedy-K           expand the K highest-score revealed parents first
//	depth-K            prefer deepest frontier nodes (dive) before breadth
func CompilePolicy(code string) (Policy, error) {
	switch normalize(code) {
	case "seed":
		return SeedPolicy{}, nil
	case "rho-greedy", "rhogreedy":
		return RhoGreedyPolicy{}, nil
	case "greedy-1", "greedy-2", "greedy-3", "greedy-4":
		var k int
		fmt.Sscanf(code, "greedy-%d", &k)
		return GreedyKPolicy{K: k}, nil
	case "depth-2", "depth-3":
		var k int
		fmt.Sscanf(code, "depth-%d", &k)
		return DepthKPolicy{K: k}, nil
	default:
		return nil, fmt.Errorf("unknown policy %q (supported: seed, rho-greedy, greedy-K, depth-K)", code)
	}
}

func normalize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == ' ' || r == '_' {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// GreedyKPolicy expands at most K frontier nodes per round, choosing the ones
// whose REVEALED children scored best so far (score-weighted ordering).
type GreedyKPolicy struct{ K int }

func (p GreedyKPolicy) Decide(v View, rho float64) ([]int64, bool) {
	if v.Calls >= int(rho*float64(v.World.B)) {
		return nil, true
	}
	// order frontier by the best revealed score among their children
	type cand struct {
		id int64
		s  float64
	}
	var cands []cand
	for _, id := range v.Frontier {
		s := 0.0
		for _, cid := range v.ChildrenOf(id) {
			if v.Revealed[cid] {
				if sc := v.scoreOf(cid); sc > s {
					s = sc
				}
			}
		}
		cands = append(cands, cand{id, s})
	}
	// sort desc by s, stable by id
	for i := 1; i < len(cands); i++ {
		for j := i; j > 0 && (cands[j].s > cands[j-1].s || (cands[j].s == cands[j-1].s && cands[j].id < cands[j-1].id)); j-- {
			cands[j], cands[j-1] = cands[j-1], cands[j]
		}
	}
	if len(cands) > p.K {
		cands = cands[:p.K]
	}
	out := make([]int64, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.id)
	}
	return out, false
}

// DepthKPolicy dives the deepest frontier branch first before spreading.
type DepthKPolicy struct{ K int }

func (p DepthKPolicy) Decide(v View, rho float64) ([]int64, bool) {
	if v.Calls >= int(rho*float64(v.World.B)) {
		return nil, true
	}
	depth := map[int64]int{v.World.Root: 0}
	// compute depths over revealed nodes via BFS from root. visited guards
	// against cycles: real exploration graphs can carry parent loops (the
	// structural edges are recorded as-written), and an unguarded queue grew
	// forever — the hang behind the first live dream run.
	visited := map[int64]bool{v.World.Root: true}
	order := []int64{v.World.Root}
	for len(order) > 0 {
		cur := order[0]
		order = order[1:]
		for _, n := range v.World.Nodes {
			if n.Parent == cur && v.Revealed[n.ID] && !visited[n.ID] {
				visited[n.ID] = true
				depth[n.ID] = depth[cur] + 1
				order = append(order, n.ID)
			}
		}
	}
	type cand struct {
		id int64
		d  int
	}
	var cands []cand
	for _, id := range v.Frontier {
		cands = append(cands, cand{id, depth[id]})
	}
	for i := 1; i < len(cands); i++ {
		for j := i; j > 0 && cands[j].d > cands[j-1].d; j-- {
			cands[j], cands[j-1] = cands[j-1], cands[j]
		}
	}
	if len(cands) > p.K {
		cands = cands[:p.K]
	}
	out := make([]int64, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.id)
	}
	return out, false
}

// scoreOf returns a node's recorded score from the view's score index.
func (v View) scoreOf(id int64) float64 { return v.scores[id] }

// FilterWorlds scopes a world list to the requested ids. Nil/empty request
// returns all worlds (the historical default). A request that matches nothing
// errors: a stale world id must never silently widen a scoped evaluation back
// to all worlds.
func FilterWorlds(worlds []map[string]any, want []int64) ([]map[string]any, error) {
	if len(worlds) == 0 {
		return nil, fmt.Errorf("no replay worlds; export completed tasks first")
	}
	if len(want) == 0 {
		return worlds, nil
	}
	allow := map[int64]bool{}
	for _, id := range want {
		allow[id] = true
	}
	out := make([]map[string]any, 0, len(want))
	for _, wm := range worlds {
		if allow[wm["id"].(int64)] {
			out = append(out, wm)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("none of the selected worlds exist")
	}
	return out, nil
}
