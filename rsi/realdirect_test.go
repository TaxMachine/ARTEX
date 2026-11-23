package rsi

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestRealWorldPolicies replays every dreamer policy over the exported real
// world (task 11) — the exact dream-path inputs — to find any hang.
func TestRealWorldPolicies(t *testing.T) {
	if os.Getenv("RSI_REAL_WORLD") == "" {
		t.Skip("set RSI_REAL_WORLD=path")
	}
	raw, err := os.ReadFile(os.Getenv("RSI_REAL_WORLD"))
	if err != nil {
		t.Fatal(err)
	}
	var w World
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	t.Logf("world: %d nodes, B=%d, S=%.3f", len(w.Nodes), w.B, w.S)
	policies := map[string]Policy{
		"seed":       SeedPolicy{},
		"rho-greedy": RhoGreedyPolicy{},
		"greedy-1":   GreedyKPolicy{K: 1},
		"greedy-2":   GreedyKPolicy{K: 2},
		"greedy-3":   GreedyKPolicy{K: 3},
		"greedy-4":   GreedyKPolicy{K: 4},
		"depth-2":    DepthKPolicy{K: 2},
		"depth-3":    DepthKPolicy{K: 3},
	}
	for name, p := range policies {
		for _, rho := range []float64{0, 0.25, 0.5, 0.75, 1} {
			done := make(chan time.Duration, 1)
			go func() {
				st := time.Now()
				Replay(&w, p, rho, 20)
				done <- time.Since(st)
			}()
			select {
			case d := <-done:
				if d > time.Second {
					t.Errorf("%s rho=%v SLOW: %v", name, rho, d)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s rho=%v HANGS (>5s)", name, rho)
			}
		}
		t.Logf("%s: all rhos ok", name)
	}
}
