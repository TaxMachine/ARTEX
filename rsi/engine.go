package rsi

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

// Dream-RSI core for ARTEX (arXiv 2609.14858, adapted).
//
// The discovery tree is a completed task's exploration graph: intents/facts/
// findings/goals as nodes, derived-from edges as parent links. The fixed
// evaluator maps each node to a quality in [0,1]; cost is the recorded token +
// latency spend. A replay world is the tree + scores frozen after a task ends.

// Node is one scored vertex of a replay world.
type Node struct {
	ID     int64   `json:"id"`
	Kind   string  `json:"kind"`
	Parent int64   `json:"parent"` // 0 = root
	Score  float64 `json:"score"`  // evaluator quality, 0..1
	Cost   float64 `json:"cost"`   // cost units (1e3 tokens)
	At     int64   `json:"at"`     // unix seconds
}

// World is a frozen discovery tree ready for replay.
type World struct {
	ID      int64   `json:"id"`
	TaskID  int64   `json:"task_id"`
	Name    string  `json:"name"`
	Root    int64   `json:"root"`
	Nodes   []Node  `json:"nodes"`
	B       int     `json:"B"` // non-root node count
	S       float64 `json:"S"` // best recorded score
	CostSum float64 `json:"cost_sum"`
}

// Reveal is one replay step outcome.
type Reveal struct {
	Nodes  []int64 `json:"revealed"` // node ids revealed this round
	Score  float64 `json:"score"`    // best score after reveal
	Calls  int     `json:"calls"`    // cumulative agent calls (nodes revealed)
	Rounds int     `json:"rounds"`
}

// EvaluateNode computes the fixed evaluator's quality for one exploration
// node. Composite, deterministic, version-pinned by the caller:
//   - finding: severity-weighted exploit value (confirmed chains highest)
//   - fact: confidence + information gain proxies (length discipline, links)
//   - intent: none of its own (structure only) — small base for effort
//   - goal/hint/digest: 0 (bookkeeping)
//
// This is ARTEX's "runtime/speedup" equivalent: our discovery reward is real
// vulnerability value, approximated structurally until primitives carry
// exploitability scores natively.
func EvaluateNode(kind string, payload map[string]any, children int, cost float64) (quality float64, components map[string]any) {
	components = map[string]any{}
	switch kind {
	case "finding":
		sev := map[string]float64{"critical": 1.0, "high": 0.8, "medium": 0.55, "low": 0.35}[str(payload, "severity")]
		status := str(payload, "status")
		confirm := 0.6
		if status == "confirmed" || status == "fixed" {
			confirm = 1.0
		} else if status == "false_positive" {
			confirm = 0.0
		}
		chain := 0.0
		if chainSteps(payload) > 1 {
			chain = 0.3 // composed chain beats single primitive
		}
		quality = clamp01(0.5*sev + 0.35*confirm + 0.15*(0.5+chain))
		components = map[string]any{"severity": sev, "confirmed": confirm, "chain": chain}
	case "fact":
		conf := map[string]float64{"observed": 1.0, "inferred": 0.6}[str(payload, "confidence")]
		if conf == 0 {
			conf = 0.5
		}
		links := float64(children)
		info := clamp01(float64(len(str(payload, "detail"))) / 2000.0) // information volume, capped
		quality = clamp01(0.5*conf + 0.3*info + 0.2*clamp01(links/4.0))
		components = map[string]any{"confidence": conf, "info": info, "links": links}
	case "intent":
		quality = 0.02 // structure value only; children carry the reward
	default:
		quality = 0
	}
	return quality, components
}

// Replay executes one policy over a world: deterministic re-traversal.
// The policy sees the revealed subtree each round and selects which revealed
// nodes to expand (their recorded children become revealed next round) and
// whether to stop. rho in [0,1] biases budget preference (0 = stop early,
// 1 = exhaust). Returns the (calls, bestScore) trajectory.
func Replay(w *World, policy Policy, rho float64, maxRounds int) []Reveal {
	if maxRounds <= 0 {
		maxRounds = 20
	}
	children := map[int64][]int64{}
	byID := map[int64]Node{}
	scores := map[int64]float64{}
	for _, n := range w.Nodes {
		byID[n.ID] = n
		scores[n.ID] = n.Score
		children[n.Parent] = append(children[n.Parent], n.ID)
	}
	for p := range children {
		sort.Slice(children[p], func(i, j int) bool {
			a, b := children[p][i], children[p][j]
			ka, kb := kindRank(byID[a].Kind), kindRank(byID[b].Kind)
			if ka != kb {
				return ka < kb
			}
			return byID[a].At < byID[b].At
		})
	}
	revealed := map[int64]bool{w.Root: true}
	best := 0.0
	calls := 0
	rounds := 0
	var traj []Reveal
	for k := 0; k < maxRounds; k++ {
		// selectable: revealed nodes with un-revealed children (the frontier).
		var frontier []int64
		for id := range revealed {
			for _, c := range children[id] {
				if !revealed[c] {
					frontier = append(frontier, id)
					break
				}
				_ = c
			}
		}
		if len(frontier) == 0 {
			break
		}
		sort.Slice(frontier, func(i, j int) bool { return frontier[i] < frontier[j] })
		view := View{World: w, Revealed: revealed, Frontier: frontier, Best: best, Calls: calls, Round: k, children: children, scores: scores}
		selectIDs, stop := policy.Decide(view, rho)
		if stop || len(selectIDs) == 0 {
			break
		}
		var newly []int64
		for _, id := range selectIDs {
			if !revealed[id] {
				continue
			}
			for _, c := range children[id] {
				if !revealed[c] {
					revealed[c] = true
					newly = append(newly, c)
					calls++
					if s := byID[c].Score; s > best {
						best = s
					}
				}
			}
		}
		rounds++
		traj = append(traj, Reveal{Nodes: newly, Score: best, Calls: calls, Rounds: rounds})
		if calls >= w.B {
			break
		}
	}
	return traj
}

// Objective computes V_i^m (eq. 1 adapted): normalized hypervolume of the
// quality-per-cost Pareto frontier over the rho sweep, minus the parallelism
// penalty (rounds per call).
func Objective(replays map[float64][]Reveal, B int, lambda float64) float64 {
	type point struct {
		n int
		a float64
	}
	var pts []point
	totalRounds, roundsWithCalls, rhoWithCalls := 0.0, 0.0, 0.0
	for rho, traj := range replays {
		_ = rho
		if len(traj) == 0 {
			continue
		}
		last := traj[len(traj)-1]
		pts = append(pts, point{last.Calls, last.Score})
		if last.Calls > 0 {
			roundsWithCalls++
			totalRounds += float64(last.Rounds)
		}
		rhoWithCalls++
	}
	if len(pts) == 0 || B <= 0 {
		return 0
	}
	// nondominated frontier
	sort.Slice(pts, func(i, j int) bool {
		if pts[i].n != pts[j].n {
			return pts[i].n < pts[j].n
		}
		return pts[i].a > pts[j].a
	})
	var front []point
	best := -1.0
	for _, p := range pts {
		if p.a > best {
			front = append(front, p)
			best = p.a
		}
	}
	// normalized hypervolume: sum over frontier steps of (a_j - a_{j-1}) * (B - n_j)
	hv := 0.0
	prev := 0.0
	for _, p := range front {
		hv += (p.a - prev) * float64(B-p.n)
		prev = p.a
	}
	hv /= float64(B) // normalize by B (scores already 0..1)
	// parallelism penalty
	penalty := 0.0
	if roundsWithCalls > 0 {
		penen := totalRounds / roundsWithCalls
		totalCalls := 0.0
		for _, p := range pts {
			totalCalls += float64(p.n)
		}
		if totalCalls > 0 {
			penalty = penen / (totalCalls / float64(len(replays)))
		}
	}
	_ = rhoWithCalls
	return hv - lambda*penalty
}

// ---------- policy interface + seed policies ----------

// View is what a policy may condition on (recorded outcomes only).
type View struct {
	World    *World
	Revealed map[int64]bool
	Frontier []int64 // revealed ids with un-revealed children
	Best     float64
	Calls    int
	Round    int
	children map[int64][]int64 // index for policies (unexported)
	scores   map[int64]float64
}

// ChildrenOf returns the recorded children of id (revealed or not).
func (v View) ChildrenOf(id int64) []int64 { return v.children[id] }

// Policy decides the next round's expansions. It must be deterministic.
type Policy interface {
	Decide(v View, rho float64) (selectIDs []int64, stop bool)
}

// SeedPolicy mirrors the ARTEX planner's historical behavior: breadth-first
// over the frontier, expand everything, never stop early (rho scales nothing —
// the recorded planner didn't condition on budget).
type SeedPolicy struct{}

func (SeedPolicy) Decide(v View, rho float64) ([]int64, bool) {
	_ = rho
	return v.Frontier, false
}

// RhoGreedyPolicy stops/continues on rho: expand the whole frontier but stop
// when the revealed fraction exceeds rho (0 = stop immediately, 1 = exhaust).
type RhoGreedyPolicy struct{}

func (RhoGreedyPolicy) Decide(v View, rho float64) ([]int64, bool) {
	if v.Calls >= int(rho*float64(v.World.B)) {
		return nil, true
	}
	return v.Frontier, false
}

// ---------- helpers ----------

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func chainSteps(payload map[string]any) int {
	if raw, ok := payload["chain"].([]any); ok {
		return len(raw)
	}
	if raw, ok := payload["primitives"].([]any); ok {
		return len(raw)
	}
	return 0
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func kindRank(k string) int {
	switch k {
	case "finding":
		return 0
	case "fact":
		return 1
	case "intent":
		return 2
	default:
		return 3
	}
}

var _ = fmt.Sprintf
var _ = math.Abs
var _ = json.Marshal
var _ = time.Now
