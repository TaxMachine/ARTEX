package agent

import (
	"strings"
	"testing"

	"github.com/TaxMachine/ARTEX/third_party/norma/llm"
)

func dietMsgs(nTools int, resultLen int) []llm.Message {
	msgs := []llm.Message{llm.UserText("go")}
	for i := 0; i < nTools; i++ {
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockThinking, Thinking: strings.Repeat("t", 900), Signature: "sig"},
			{Type: llm.BlockToolUse, ID: string(rune('a' + i)), Name: "Bash", Input: []byte(`{"cmd":"ls"}`)},
		}})
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{
			{Type: llm.BlockToolResult, ToolUseID: string(rune('a' + i)), Content: []llm.ContentBlock{llm.TextBlock(strings.Repeat("x", resultLen))}},
		}})
	}
	msgs = append(msgs, llm.UserText("summarize"))
	return msgs
}

func TestDietElidesOldToolResultsKeepsRecent(t *testing.T) {
	cfg := HistoryDietConfig{Enabled: true, Keep: 2, Head: 100}
	out, st := DietMessages(dietMsgs(6, 5000), cfg)
	if st.ElidedResults != 4 {
		t.Fatalf("expected 4 elided, got %d", st.ElidedResults)
	}
	// the last 2 results stay verbatim
	kept := 0
	elided := 0
	for _, m := range out {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult {
				txt := flattenToolResultText(b)
				if strings.Contains(txt, "history diet") {
					elided++
				} else if len(txt) == 5000 {
					kept++
				}
			}
		}
	}
	if kept != 2 || elided != 4 {
		t.Fatalf("kept=%d elided=%d", kept, elided)
	}
	if st.OriginalChars <= st.DietedChars {
		t.Fatalf("diet must shrink: %d -> %d", st.OriginalChars, st.DietedChars)
	}
}

func TestDietNeverMutatesInput(t *testing.T) {
	msgs := dietMsgs(6, 5000)
	before := dietMessageChars(msgs[len(msgs)-3])
	_, _ = DietMessages(msgs, HistoryDietConfig{Enabled: true, Keep: 1, Head: 50, DropThinking: true})
	if got := dietMessageChars(msgs[len(msgs)-3]); got != before {
		t.Fatalf("input mutated: %d -> %d", before, got)
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && strings.Contains(flattenToolResultText(b), "history diet") {
				t.Fatal("diet leaked into the input slice")
			}
		}
	}
}

func TestDietPreservesToolPairing(t *testing.T) {
	msgs := dietMsgs(6, 5000)
	out, _ := DietMessages(msgs, HistoryDietConfig{Enabled: true, Keep: 2, Head: 50, DropThinking: true})
	// every tool_use id must still find a tool_result with the same id
	calls := map[string]bool{}
	results := map[string]bool{}
	for _, m := range out {
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolUse:
				calls[b.ID] = true
			case llm.BlockToolResult:
				results[b.ToolUseID] = true
			}
		}
	}
	if len(calls) != 6 || len(results) != 6 {
		t.Fatalf("pairing broken: %d calls, %d results", len(calls), len(results))
	}
	for id := range calls {
		if !results[id] {
			t.Fatalf("tool_use %s lost its result", id)
		}
	}
}

func TestDietKeepsLastAssistantThinking(t *testing.T) {
	msgs := dietMsgs(4, 5000)
	out, st := DietMessages(msgs, HistoryDietConfig{Enabled: true, Keep: 1, Head: 50, DropThinking: true})
	// 4 thinking blocks; only the last assistant turn's survives
	survived := 0
	for _, m := range out {
		for _, b := range m.Content {
			if b.Type == llm.BlockThinking {
				survived++
			}
		}
	}
	if survived != 1 {
		t.Fatalf("expected exactly the last assistant thinking kept, got %d", survived)
	}
	if st.DroppedThinking != 3 {
		t.Fatalf("expected 3 dropped, got %d", st.DroppedThinking)
	}
}

func TestDietDisabledPassthrough(t *testing.T) {
	msgs := dietMsgs(4, 5000)
	out, st := DietMessages(msgs, HistoryDietConfig{})
	if len(out) != len(msgs) || st.ElidedResults != 0 {
		t.Fatal("disabled diet must pass through")
	}
}

func TestDietShortResultsUntouched(t *testing.T) {
	msgs := dietMsgs(3, 80) // shorter than Head
	out, st := DietMessages(msgs, HistoryDietConfig{Enabled: true, Keep: 1, Head: 100})
	if st.ElidedResults != 0 {
		t.Fatalf("short results must not be elided, got %d", st.ElidedResults)
	}
	_ = out
}
