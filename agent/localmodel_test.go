package agent

import (
	"os"
	"testing"
)

func TestLocalModelLevers(t *testing.T) {
	t.Setenv("ARTEX_TOOL_OUT_CHARS", "8000")
	if got := toolOutputChars(); got != 8000 {
		t.Fatalf("toolOutputChars = %d, want 8000", got)
	}
	t.Setenv("ARTEX_TOOL_OUT_CHARS", "100") // below floor -> default
	if got := toolOutputChars(); got != 0 {
		t.Fatalf("toolOutputChars floor = %d, want 0", got)
	}
	t.Setenv("ARTEX_COMPACTION_K", "64")
	if got := localCompactionWindowK(); got != 64 {
		t.Fatalf("localCompactionWindowK = %d, want 64", got)
	}
	// never widens a smaller resolved window
	if got := localWindowOverride(32000); got != 32000 {
		t.Fatalf("localWindowOverride(32k) = %d, want 32000", got)
	}
	if got := localWindowOverride(200000); got != 64000 {
		t.Fatalf("localWindowOverride(200k) = %d, want 64000", got)
	}
	t.Setenv("ARTEX_TOOL_DESC_MAX", "600")
	if got := toolDescMax(); got != 600 {
		t.Fatalf("toolDescMax = %d, want 600", got)
	}
	if workerKeepTools() != nil {
		t.Fatal("workerKeepTools should be nil when unset")
	}
	os.Unsetenv("ARTEX_WORKER_GLOBAL_TOOLS")
}
