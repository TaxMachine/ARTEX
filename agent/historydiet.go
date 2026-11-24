package agent

import (
	"context"
	"iter"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/TaxMachine/ARTEX/third_party/norma/llm"
	"github.com/TaxMachine/ARTEX/third_party/norma/transcript"
)

// History diet: a provider decorator that shrinks what is REPLAYED to the model
// on each call, without touching the stored transcript.
//
// An agent session resends its whole history on every turn, so input cost grows
// quadratically with turns. Measured on a long worker session (task 11): the
// replayed history reached 385K characters, 51% of it old tool results and 25%
// the model's own past thinking — content already consumed (the worker's
// durable record lives in facts/findings) but paid for again on every later
// call. The caching layer absorbs ~86% of the dollar cost but not the token
// volume, the rate limits, or the request latency.
//
// The diet changes only the wire copy:
//   - tool results older than the most recent keepN are replaced by a short
//     head plus a pointer note, so the model knows what the call returned and
//     can re-run it when it needs the detail;
//   - with DROP_THINKING, thinking blocks from PRIOR turns are dropped (the
//     gateway verified to accept requests without reasoning replay; leave off
//     for DeepSeek-style APIs that hard-require it).
//
// Structural invariants are preserved: every tool_use keeps its tool_result
// pair (only the result text shrinks), and the LAST message is never touched.
//
// Env knobs (all default OFF — existing deployments unchanged):
//
//	ARTEX_HISTORY_DIET=1                 enable
//	ARTEX_HISTORY_DIET_KEEP=8            most recent tool results kept verbatim
//	ARTEX_HISTORY_DIET_HEAD=300          chars kept from an elided result
//	ARTEX_HISTORY_DIET_DROP_THINKING=1   also drop prior-turn thinking

// HistoryDietConfig is the resolved diet configuration.
type HistoryDietConfig struct {
	Enabled      bool
	DropThinking bool
	Keep         int
	Head         int
}

// HistoryDietStats accumulates how much the diet removed, per session key.
type HistoryDietStats struct {
	Calls           int64 `json:"calls"`
	OriginalChars   int64 `json:"original_chars"`
	DietedChars     int64 `json:"dieted_chars"`
	ElidedResults   int64 `json:"elided_results"`
	DroppedThinking int64 `json:"dropped_thinking"`
}

func dietEnvInt(name string, dflt int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return dflt
}

func dietEnvOn(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// LoadHistoryDiet resolves the diet configuration (read per call so an env
// change takes effect on the next request).
func LoadHistoryDiet() HistoryDietConfig {
	return HistoryDietConfig{
		Enabled:      dietEnvOn("ARTEX_HISTORY_DIET"),
		DropThinking: dietEnvOn("ARTEX_HISTORY_DIET_DROP_THINKING"),
		Keep:         dietEnvInt("ARTEX_HISTORY_DIET_KEEP", 8),
		Head:         dietEnvInt("ARTEX_HISTORY_DIET_HEAD", 300),
	}
}

// DietMessages returns the wire copy of msgs under cfg. It never mutates the
// input: untouched messages are shared, shrunken ones are new values. Returns
// the dieted messages and how much was removed.
func DietMessages(msgs []llm.Message, cfg HistoryDietConfig) ([]llm.Message, HistoryDietStats) {
	var st HistoryDietStats
	if !cfg.Enabled || len(msgs) < 2 {
		return msgs, st
	}
	// Positions of every tool_result, in order; the last keepN stay verbatim.
	type slot struct{ mi, bi int }
	var results []slot
	for i := range msgs {
		for j := range msgs[i].Content {
			if msgs[i].Content[j].Type == llm.BlockToolResult {
				results = append(results, slot{i, j})
			}
		}
	}
	elide := map[slot]bool{}
	if len(results) > cfg.Keep {
		for _, s := range results[:len(results)-cfg.Keep] {
			elide[s] = true
		}
	}
	// The final message is the current turn — never touched. Thinking in the
	// most recent assistant turn is also kept: Anthropic-style APIs require it
	// back during a tool-use loop, and it is the reasoning behind the pending
	// tool calls, so it is the one block of past thinking still load-bearing.
	last := len(msgs) - 1
	lastAssistant := -1
	for i := last; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant {
			lastAssistant = i
			break
		}
	}
	out := msgs
	cloned := false
	for i := 0; i < last; i++ {
		var changed bool
		content := make([]llm.ContentBlock, 0, len(msgs[i].Content))
		for j := range msgs[i].Content {
			b := msgs[i].Content[j]
			switch {
			case b.Type == llm.BlockThinking && cfg.DropThinking && i != lastAssistant:
				st.DroppedThinking++
				changed = true
				continue // dropped from the wire copy
			case b.Type == llm.BlockToolResult && elide[slot{i, j}]:
				text := flattenToolResultText(b)
				if len(text) > cfg.Head {
					note := "\n…[history diet: " + strconv.Itoa(len(text)-cfg.Head) + " chars elided from this old tool result; re-run the tool if you need the full output]"
					nb := b
					nb.Content = []llm.ContentBlock{llm.TextBlock(text[:cfg.Head] + note)}
					st.ElidedResults++
					content = append(content, nb)
					changed = true
					continue
				}
			}
			content = append(content, b)
		}
		if changed {
			if !cloned {
				// Copy-on-write: the caller's slice (and the transcript it
				// may alias) is never mutated.
				out = append([]llm.Message(nil), msgs...)
				cloned = true
			}
			st.OriginalChars += int64(dietMessageChars(msgs[i]))
			out[i] = llm.Message{Role: msgs[i].Role, Content: content}
			st.DietedChars += int64(dietMessageChars(out[i]))
		}
	}
	return out, st
}

func flattenToolResultText(b llm.ContentBlock) string {
	if len(b.Content) == 0 {
		return b.Text
	}
	var sb strings.Builder
	for _, c := range b.Content {
		if c.Type == llm.BlockText {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

func dietBlockChars(b llm.ContentBlock) int {
	switch b.Type {
	case llm.BlockText:
		return len(b.Text)
	case llm.BlockThinking:
		return len(b.Thinking)
	case llm.BlockToolUse:
		return len(b.Name) + len(b.Input)
	case llm.BlockToolResult:
		n := 0
		for _, c := range b.Content {
			n += dietBlockChars(c)
		}
		return n
	}
	return 0
}

func dietMessageChars(m llm.Message) int {
	n := 0
	for _, b := range m.Content {
		n += dietBlockChars(b)
	}
	return n
}

// ---- provider decorator ----

var (
	historyDietMu    sync.Mutex
	historyDietAgg   = map[string]*HistoryDietStats{}
	historyDietForce func() HistoryDietConfig
)

// dietingProvider wraps a provider, dieting req.Messages before each call.
type dietingProvider struct {
	inner llm.Provider
}

func (d *dietingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	req = d.diet(ctx, req)
	return d.inner.Complete(ctx, req)
}

func (d *dietingProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	req = d.diet(ctx, req)
	return d.inner.Stream(ctx, req)
}

func (d *dietingProvider) diet(ctx context.Context, req llm.CompletionRequest) llm.CompletionRequest {
	cfg := LoadHistoryDiet()
	if !cfg.Enabled || len(req.Messages) < 2 {
		return req
	}
	out, st := DietMessages(req.Messages, cfg)
	if st.ElidedResults == 0 && st.DroppedThinking == 0 {
		return req
	}
	key := transcript.SessionIDFrom(ctx)
	historyDietMu.Lock()
	c := historyDietAgg[key]
	if c == nil {
		c = &HistoryDietStats{}
		historyDietAgg[key] = c
	}
	c.Calls++
	c.OriginalChars += st.OriginalChars
	c.DietedChars += st.DietedChars
	c.ElidedResults += st.ElidedResults
	c.DroppedThinking += st.DroppedThinking
	historyDietMu.Unlock()
	req.Messages = out
	return req
}

// HistoryDietSnapshot returns the accumulated per-session counters.
func HistoryDietSnapshot() map[string]HistoryDietStats {
	historyDietMu.Lock()
	defer historyDietMu.Unlock()
	out := make(map[string]HistoryDietStats, len(historyDietAgg))
	for k, v := range historyDietAgg {
		out[k] = *v
	}
	return out
}

// WithHistoryDiet wraps prov so each call's replayed history is dieted. The
// configuration is re-read on every call; a disabled diet passes through.
func WithHistoryDiet(prov llm.Provider) llm.Provider {
	return &dietingProvider{inner: prov}
}
