package agent

import "strings"
import "testing"

// The token-discipline block is code-owned and appended to every role's system
// prompt after the editable body — a DB override or a seeded legacy row can
// never drop it, and it never renders twice.
func TestOutputStyleTailOnEveryRole(t *testing.T) {
	prev := PromptOverride()
	t.Cleanup(func() { SetPromptOverride(prev) })
	SetPromptOverride(func(string) (string, bool) { return "BARE BODY", true })

	for role, sys := range map[string]string{
		"worker":    workerSystem("", "", "/data", "/run"),
		"planner":   plannerSystem("goal", "/data", "/work"),
		"mainagent": mainAgentSystem("goal", "/data", "/work"),
		"auto":      chatSystem("auto", "/data", "/work"),
		"pentest":   chatSystem("pentest", "/data", "/work"),
		"custom":    chatSystem("custom-assistant", "/data", "/work"),
	} {
		if n := strings.Count(sys, "Output style (token discipline"); n != 1 {
			t.Errorf("%s: output-style block rendered %d times, want 1", role, n)
		}
	}
	if !strings.Contains(compressionSystem, "Output style (token discipline") {
		t.Error("compactor: missing output-style block")
	}
}

// A legacy body that already carries the block (seeded before the move) does
// not get it twice.
func TestOutputStyleTailNotDuplicated(t *testing.T) {
	prev := PromptOverride()
	t.Cleanup(func() { SetPromptOverride(prev) })
	SetPromptOverride(func(string) (string, bool) {
		return "LEGACY\n" + outputStyleBlock, true
	})
	if n := strings.Count(workerSystem("", "", "/data", "/run"), "Output style (token discipline"); n != 1 {
		t.Fatalf("legacy body: block rendered %d times, want 1", n)
	}
}
