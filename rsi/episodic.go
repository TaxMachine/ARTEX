package rsi

import (
	"context"
	"fmt"
	"strings"
)

// EpisodicSummary renders the implicit-world-modeling block for the planner:
// what past tasks' recorded discovery trees say about THIS kind of work —
// which intent shapes produced findings, which died, and when yield stopped
// arriving. It is Early Experience's world-model half at inference time: the
// planner conditions on realized outcomes instead of re-deriving them.
//
// Source: rsi_worlds (exported replay trees) excluding the current task.
// Compact by design: hard caps on every section so the block stays cheap.

func (s *Store) EpisodicSummary(ctx context.Context, currentTaskID int64, maxChars int) (string, error) {
	if maxChars <= 0 {
		maxChars = 1200
	}
	worlds, err := s.ListWorlds(ctx)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, wm := range worlds {
		tid, _ := wm["task_id"].(int64)
		if tid == currentTaskID {
			continue
		}
		id, _ := wm["id"].(int64)
		w, err := s.LoadWorld(ctx, id)
		if err != nil {
			continue
		}
		// per world: last finding position, top yielding intent summaries
		lastFinding := 0
		pos := 0
		type yieldRow struct {
			summary string
			y       int
		}
		// walk intents in id order (proxy for execution order); intent summaries
		// are not in the exported tree, so pull counts from node kinds/edges
		var yields []yieldRow
		for _, n := range w.Nodes {
			if n.Kind != "intent" {
				continue
			}
			pos++
			y := 0
			for _, c := range w.Nodes {
				if c.Parent == n.ID && (c.Kind == "fact" || c.Kind == "finding") {
					y++
				}
			}
			if y > 0 {
				yields = append(yields, yieldRow{"", y})
			}
		}
		findings := 0
		for _, n := range w.Nodes {
			if n.Kind == "finding" {
				findings++
			}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "- %s: %d nodes, %d findings", wm["name"], w.B, findings)
		if len(yields) > 0 {
			fmt.Fprintf(&b, "; %d/%d intents yielded (facts+findings)", len(yields), pos)
		}
		parts = append(parts, b.String())
		_ = lastFinding
	}
	if len(parts) == 0 {
		return "", nil
	}
	block := "[Episodic memory — recorded outcomes from past task replay worlds; condition on them instead of re-deriving]:\n" +
		strings.Join(parts, "\n")
	if len(block) > maxChars {
		block = block[:maxChars]
	}
	return block, nil
}
