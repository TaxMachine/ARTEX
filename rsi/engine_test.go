package rsi

import (
	"math/rand"
	"testing"
)

// buildWorld makes a synthetic discovery tree: root -> 3 intents, each with
// facts, one branch carrying a finding.
func buildWorld(seed int64) *World {
	r := rand.New(rand.NewSource(seed))
	w := &World{Root: 1}
	next := int64(2)
	intent := func() int64 {
		id := next
		next++
		w.Nodes = append(w.Nodes, Node{ID: id, Kind: "intent", Parent: w.Root, Score: 0.02, Cost: 3, At: next})
		return id
	}
	fact := func(parent int64, score float64) {
		id := next
		next++
		w.Nodes = append(w.Nodes, Node{ID: id, Kind: "fact", Parent: parent, Score: score, Cost: 1 + r.Float64(), At: next})
	}
	for i := 0; i < 3; i++ {
		pi := intent()
		for f := 0; f < 4; f++ {
			fact(pi, 0.1+0.6*r.Float64())
		}
	}
	// one real finding deep in the first branch
	f0 := w.Nodes[1].ID // first intent
	id := next
	next++
	w.Nodes = append(w.Nodes, Node{ID: id, Kind: "finding", Parent: f0, Score: 0.95, Cost: 5, At: next})
	w.B = len(w.Nodes)
	w.S = 0.95
	return w
}

func TestReplayDeterministicAndBounded(t *testing.T) {
	w := buildWorld(7)
	for _, rho := range []float64{0, 0.25, 0.5, 0.75, 1} {
		a := Replay(w, SeedPolicy{}, rho, 20)
		b := Replay(w, SeedPolicy{}, rho, 20)
		if len(a) != len(b) {
			t.Fatalf("rho=%v: replay not deterministic: %d vs %d rounds", rho, len(a), len(b))
		}
		for i := range a {
			if a[i].Calls != b[i].Calls || a[i].Score != b[i].Score {
				t.Fatalf("rho=%v round %d: %v vs %v", rho, i, a[i], b[i])
			}
		}
		last := a[len(a)-1]
		if last.Calls > w.B {
			t.Fatalf("rho=%v: calls %d exceed recorded nodes %d (replay must stay inside the tree)", rho, last.Calls, w.B)
		}
		if last.Score > w.S+1e-9 {
			t.Fatalf("rho=%v: revealed score %v exceeds recorded best %v", rho, last.Score, w.S)
		}
	}
}

func TestRhoGreedyRespectsBudget(t *testing.T) {
	w := buildWorld(3)
	// rho=0 must spend nothing: the budget is 0 calls.
	if traj := Replay(w, RhoGreedyPolicy{}, 0, 20); len(traj) != 0 {
		t.Fatalf("rho=0 should stop immediately, got %d rounds", len(traj))
	}
	// rho=1 must reveal everything reachable.
	traj := Replay(w, RhoGreedyPolicy{}, 1, 20)
	if len(traj) == 0 || traj[len(traj)-1].Calls != w.B {
		t.Fatalf("rho=1 should reveal all %d nodes, got %+v", w.B, traj)
	}
}

func TestObjectiveRewardsQualityPerCall(t *testing.T) {
	w := buildWorld(7)
	sweep := []float64{0, 0.25, 0.5, 0.75, 1}
	run := func(p Policy) map[float64][]Reveal {
		out := map[float64][]Reveal{}
		for _, rho := range sweep {
			out[rho] = Replay(w, p, rho, 20)
		}
		return out
	}
	vSeed := Objective(run(SeedPolicy{}), w.B, 0.1)
	vRho := Objective(run(RhoGreedyPolicy{}), w.B, 0.1)
	// SeedPolicy ignores rho (same trajectory 5x); RhoGreedy traces a real
	// frontier across budgets, so its hypervolume must be >=.
	if vRho < vSeed-1e-9 {
		t.Fatalf("RhoGreedy objective %v below Seed %v; frontier should dominate", vRho, vSeed)
	}
	// SeedPolicy expands everything at every rho: its frontier is the single
	// full-budget point, so the hypervolume term is ~0 and the parallelism
	// penalty can push it slightly negative. That is the paper's point about
	// fixed exploration — budget-aware policies dominate it.
	if vSeed < -0.5 || vSeed > 1.5 {
		t.Fatalf("seed objective out of plausible range: %v", vSeed)
	}
	t.Logf("seed V=%v  rho-greedy V=%v (budget-aware dominates)", vSeed, vRho)
}

func TestEvaluatorBounds(t *testing.T) {
	q, _ := EvaluateNode("finding", map[string]any{"severity": "critical", "status": "confirmed", "chain": []any{1, 2}}, 0, 0)
	if q < 0.9 {
		t.Fatalf("confirmed critical chain should score high, got %v", q)
	}
	q2, _ := EvaluateNode("finding", map[string]any{"severity": "low", "status": "false_positive"}, 0, 0)
	if q2 > 0.35 {
		t.Fatalf("false positive should score low, got %v", q2)
	}
}

func TestFilterWorldsScopesAndNeverSilentlyWidens(t *testing.T) {
	ws := []map[string]any{
		{"id": int64(1)}, {"id": int64(2)}, {"id": int64(3)},
	}
	// no filter = all
	got, err := FilterWorlds(ws, nil)
	if err != nil || len(got) != 3 {
		t.Fatalf("nil filter: got %d worlds, err=%v", len(got), err)
	}
	// subset
	got, err = FilterWorlds(ws, []int64{1, 3})
	if err != nil || len(got) != 2 || got[0]["id"] != int64(1) || got[1]["id"] != int64(3) {
		t.Fatalf("subset filter: got %v, err=%v", got, err)
	}
	// unknown ids mixed with known ones: keep the known, ignore the stale
	got, err = FilterWorlds(ws, []int64{3, 99})
	if err != nil || len(got) != 1 || got[0]["id"] != int64(3) {
		t.Fatalf("stale-id filter: got %v, err=%v", got, err)
	}
	// all-unknown: error, never a silent all-worlds run
	if _, err := FilterWorlds(ws, []int64{42}); err == nil {
		t.Fatal("all-unknown filter must error, not silently evaluate all worlds")
	}
}
