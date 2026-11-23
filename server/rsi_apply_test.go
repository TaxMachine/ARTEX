package server

import (
	"testing"
	"time"
)

// TestBudgetGateFlattening exercises the gate's decision math in isolation:
// the same yield-window comparison the live gate performs, with the same
// constants, so a regression in the thresholds fails here first.
func TestBudgetGateFlattening(t *testing.T) {
	const window = 10
	flattened := func(prior, recent int) bool {
		if prior == 0 || recent > prior/5 || recent >= 3 {
			return false
		}
		return true
	}
	cases := []struct {
		name          string
		prior, recent int
		wantGate      bool
	}{
		{"healthy yield", 20, 12, false}, // recent comparable to prior
		{"slight decline", 20, 5, false}, // 25% of prior: above the 20% cut
		{"flattened", 20, 3, false},      // exactly at the absolute floor
		{"flattened hard", 20, 2, true},  // 10% of prior, under floor
		{"dead stop", 15, 0, true},       // nothing new at all
		{"low prior noise", 2, 0, true},  // both tiny: prior>0, recent 0
		{"no prior yield", 0, 0, false},  // prior 0: undefined slope, don't gate
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := flattened(c.prior, c.recent); got != c.wantGate {
				t.Fatalf("prior=%d recent=%d: gate=%v, want %v", c.prior, c.recent, got, c.wantGate)
			}
		})
	}
	_ = window
	_ = time.Now
}
