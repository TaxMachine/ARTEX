package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/TaxMachine/ARTEX/third_party/norma/llm"
)

// dietTurn appends one assistant tool-call turn + its result to msgs and
// returns the extended history (mirrors a real session growing).
func dietTurn(msgs []llm.Message, id string, resultLen, thinkLen int) []llm.Message {
	msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: strings.Repeat("t", thinkLen)},
		{Type: llm.BlockToolUse, ID: id, Name: "Bash", Input: []byte(`{"cmd":"ls"}`)},
	}})
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{
		{Type: llm.BlockToolResult, ToolUseID: id, Content: []llm.ContentBlock{llm.TextBlock(strings.Repeat("x", resultLen))}},
	}})
	return msgs
}

func dietHistory(n int, resultLen, thinkLen int) []llm.Message {
	msgs := []llm.Message{llm.UserText("go")}
	for i := 0; i < n; i++ {
		msgs = dietTurn(msgs, string(rune('a'+i)), resultLen, thinkLen)
	}
	return msgs
}

func wireJSON(t *testing.T, msgs []llm.Message) string {
	t.Helper()
	b, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// samePrefix reports whether shorter is a message-by-message prefix of longer
// (the append-only property a provider prefix cache needs). Whole-array JSON
// cannot be compared as a string prefix: the first array's closing bracket can
// never be a prefix of a longer array.
func samePrefix(t *testing.T, shorter, longer []llm.Message) bool {
	t.Helper()
	if len(longer) < len(shorter) {
		return false
	}
	for i := range shorter {
		if wireJSON(t, shorter[i:i+1]) != wireJSON(t, longer[i:i+1]) {
			return false
		}
	}
	return true
}

func TestDietStickyPrefixIsStableBetweenFreezes(t *testing.T) {
	// The whole point of sticky elision: between freezes each request is the
	// previous one plus a tail (append-only), so a provider prefix cache can
	// serve it. A freeze legitimately rewrites the prefix — it pays ONE cache
	// break — so the assertion is: after a non-freeze call the previous wire
	// copy is a strict prefix, and freezes are rare over the session.
	cfg := HistoryDietConfig{Enabled: true, Keep: 2, Head: 50, DropThinking: true}
	msgs := dietHistory(10, 2000, 500)
	st := DietFreezeState{}
	var prev []llm.Message
	freezes, appendOnly := 0, 0
	for turn := 0; turn < 12; turn++ {
		out, nst, stats := DietSticky(msgs, st, cfg)
		froze := stats.Rewrites > 0
		freezes += int(stats.Rewrites)
		st = nst
		if prev != nil && !froze {
			if !samePrefix(t, prev, out) {
				t.Fatalf("turn %d: request changed without a freeze — cache would break needlessly", turn)
			}
			appendOnly++
		}
		prev = out
		msgs = dietTurn(msgs, string(rune('A'+turn)), 2000, 500)
	}
	// with KEEP=2 a freeze runs every 2 new results: half the turns are
	// append-only, and each freeze pays exactly one cache break.
	if appendOnly < 4 {
		t.Fatalf("expected most turns append-only, got %d of 12", appendOnly)
	}
	if freezes == 0 || freezes > 8 {
		t.Fatalf("expected periodic freezes over 12 growing turns, got %d", freezes)
	}
}

func TestDietStickyShrinksAndKeepsRecentVerbatim(t *testing.T) {
	cfg := HistoryDietConfig{Enabled: true, Keep: 2, Head: 50, DropThinking: true}
	// drive it like a session: append turns one at a time so freezes trigger
	st := DietFreezeState{}
	var out []llm.Message
	for i := 1; i <= 12; i++ {
		out, st, _ = DietSticky(dietHistory(i, 3000, 400), st, cfg)
	}
	var elided, verbatim, thinking int
	for _, m := range out {
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolResult:
				if strings.Contains(flattenToolResultText(b), "history diet") {
					elided++
				} else {
					verbatim++
				}
			case llm.BlockThinking:
				thinking++
			}
		}
	}
	if elided == 0 {
		t.Fatal("nothing was elided")
	}
	if verbatim < cfg.Keep {
		t.Fatalf("fewer than KEEP=%d verbatim results (%d)", cfg.Keep, verbatim)
	}
	if dietMessageChars(out[len(out)-1]) == 0 {
		t.Fatal("last message must survive")
	}
}

func TestDietStickyPreservesToolPairing(t *testing.T) {
	cfg := HistoryDietConfig{Enabled: true, Keep: 2, Head: 50, DropThinking: true}
	st := DietFreezeState{}
	var out []llm.Message
	for i := 1; i <= 10; i++ {
		out, st, _ = DietSticky(dietHistory(i, 3000, 400), st, cfg)
	}
	calls, results := map[string]bool{}, map[string]bool{}
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
	if len(calls) != 10 || len(results) != 10 {
		t.Fatalf("pairing broken: %d calls, %d results", len(calls), len(results))
	}
}

func TestDietStickyNeverMutatesInput(t *testing.T) {
	msgs := dietHistory(10, 3000, 400)
	before := make([]int, len(msgs))
	for i := range msgs {
		before[i] = dietMessageChars(msgs[i])
	}
	st := DietFreezeState{}
	for i := 1; i <= 10; i++ {
		_, st, _ = DietSticky(dietHistory(i, 3000, 400), st, cfg)
	}
	for i := range msgs {
		if dietMessageChars(msgs[i]) != before[i] {
			t.Fatalf("input message %d mutated", i)
		}
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && strings.Contains(flattenToolResultText(b), "history diet") {
				t.Fatal("diet leaked into the input slice")
			}
		}
	}
}

func TestDietStickyDisabledPassthrough(t *testing.T) {
	msgs := dietHistory(6, 3000, 400)
	out, st, stats := DietSticky(msgs, DietFreezeState{}, HistoryDietConfig{})
	if stats.ElidedResults != 0 || len(out) != len(msgs) || st.FrozenLen != 0 {
		t.Fatal("disabled diet must pass through")
	}
	if wireJSON(t, out) != wireJSON(t, msgs) {
		t.Fatal("disabled diet must not alter messages")
	}
}

var cfg = HistoryDietConfig{Enabled: true, Keep: 2, Head: 50, DropThinking: true}
