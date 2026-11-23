package rsi

import (
	"testing"
	"time"
)

func TestReplayPerf330Nodes(t *testing.T) {
	w := buildWorld(1)
	for len(w.Nodes) < 330 {
		pid := w.Nodes[len(w.Nodes)/2].ID
		w.Nodes = append(w.Nodes, Node{ID: int64(len(w.Nodes) + 10), Kind: "fact", Parent: pid, Score: 0.3, Cost: 1})
	}
	w.B = len(w.Nodes)
	start := time.Now()
	Replay(w, GreedyKPolicy{K: 2}, 0.75, 20)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("replay over %d nodes took %v", len(w.Nodes), d)
	}
}
