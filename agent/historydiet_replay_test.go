package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"github.com/TaxMachine/ARTEX/third_party/norma/llm"
)

// TestDietReplayRealTranscript replays real recorded worker sessions through
// the diet and reports the token-volume reduction (skipped unless the dump
// directory exists; run manually after copying transcripts there).
func TestDietReplayRealTranscript(t *testing.T) {
	dir := os.Getenv("ARTEX_DIET_REPLAY_DIR")
	if dir == "" {
		t.Skip("set ARTEX_DIET_REPLAY_DIR to a directory of *.jsonl transcripts")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
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
		type variant struct {
			name string
			cfg  HistoryDietConfig
			sum  int64
		}
		variants := []variant{
			{"no diet", HistoryDietConfig{}, 0},
			{"tool elision only", HistoryDietConfig{Enabled: true, Keep: 8, Head: 300}, 0},
			{"elision + drop thinking", HistoryDietConfig{Enabled: true, Keep: 8, Head: 300, DropThinking: true}, 0},
		}
		// every assistant row was one request whose input was everything before it
		for i := range msgs {
			if msgs[i].Role != llm.RoleAssistant {
				continue
			}
			for v := range variants {
				out, _ := DietMessages(msgs[:i], variants[v].cfg)
				for _, m := range out {
					variants[v].sum += int64(dietMessageChars(m))
				}
			}
		}
		t.Logf("%s: %d messages", e.Name(), len(msgs))
		base := variants[0].sum
		for _, v := range variants {
			t.Logf("  %-26s %14d chars  (%.1f%% of baseline, saves %.1f%%)", v.name, v.sum, 100*float64(v.sum)/float64(base), 100-100*float64(v.sum)/float64(base))
		}
	}
}
