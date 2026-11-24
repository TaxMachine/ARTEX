package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/TaxMachine/ARTEX/third_party/norma/llm"
)

// TestDietStickyReplayRealTranscript replays real recorded worker sessions
// through the sticky diet and reports the token-volume reduction plus how many
// cache-breaking freezes a session would pay. Skipped unless
// ARTEX_DIET_REPLAY_DIR points at a directory of *.jsonl transcripts.
func TestDietStickyReplayRealTranscript(t *testing.T) {
	dir := os.Getenv("ARTEX_DIET_REPLAY_DIR")
	if dir == "" {
		t.Skip("set ARTEX_DIET_REPLAY_DIR to a directory of *.jsonl transcripts")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		f, err := os.Open(dir + "/" + e.Name())
		if err != nil {
			continue
		}
		var msgs []llm.Message
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for sc.Scan() {
			var row struct {
				Message json.RawMessage `json:"message"`
			}
			if json.Unmarshal(sc.Bytes(), &row) != nil {
				continue
			}
			var m llm.Message
			if json.Unmarshal(row.Message, &m) != nil {
				continue
			}
			msgs = append(msgs, m)
		}
		f.Close()
		base := int64(0)
		for i := range msgs {
			if msgs[i].Role == llm.RoleAssistant {
				for _, m := range msgs[:i] {
					base += int64(dietMessageChars(m))
				}
			}
		}
		cfg := LoadHistoryDiet()
		cfg.Enabled = true
		st := DietFreezeState{}
		sum := int64(0)
		freezes := 0
		for i := range msgs {
			if msgs[i].Role != llm.RoleAssistant {
				continue
			}
			out, nst, stats := DietSticky(msgs[:i], st, cfg)
			st = nst
			freezes += int(stats.Rewrites)
			for _, m := range out {
				sum += int64(dietMessageChars(m))
			}
		}
		t.Logf("%s: %d messages, %d freezes", e.Name(), len(msgs), freezes)
		t.Logf("  sticky diet  %14d chars (%.1f%% of baseline, saves %.1f%%)", sum, 100*float64(sum)/float64(base), 100-100*float64(sum)/float64(base))
	}
}
