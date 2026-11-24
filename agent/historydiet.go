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
// call.
//
// STICKY (generation-based) elision. The first version elided everything older
// than the most recent KEEP results; that boundary slides by one result every
// turn, so each request changed an old message and the provider's prefix cache
// re-broke at that point every call (measured: cache hit fell 74%→47%). This
// version is append-stable between freezes:
//
//   - the history is split at a FROZEN boundary. Below it, every oversized
//     tool result is elided (head + pointer note) and prior-turn thinking is
//     dropped. The frozen form is deterministic, so it is byte-identical on
//     every call — the cache prefix survives.
//   - above the boundary (the live zone) everything is verbatim and the
//     request grows append-only: each new call is the previous request plus a
//     tail, which a prefix cache serves at full hit rate.
//   - only when the live zone grows past REWRITE_CHARS (and holds more than
//     KEEP results) does one freeze run: results older than the newest KEEP
//     are elided once, the boundary advances, and the cache breaks exactly
//     once at that point.
//
// Structural invariants: every tool_use keeps its tool_result pair (only the
// result text shrinks — an orphaned tool_call is a gateway 400), the final
// message is never touched, and the newest assistant turn's thinking is kept
// while it is in the live zone.
//
// Env knobs (default OFF — existing deployments unchanged):
//
//	ARTEX_HISTORY_DIET=1                  enable
//	ARTEX_HISTORY_DIET_KEEP=8             newest tool results kept verbatim
//	ARTEX_HISTORY_DIET_HEAD=300           chars kept from an elided result
//	ARTEX_HISTORY_DIET_DROP_THINKING=1    drop prior-turn thinking when freezing
//	A freeze runs when the live zone exceeds 2*KEEP results; between freezes
//	requests grow append-only (prefix-cache stable), paying one break each.

// HistoryDietConfig is the resolved diet configuration.
type HistoryDietConfig struct {
	Enabled      bool
	DropThinking bool
	Keep         int // newest tool results kept verbatim in the live zone
	Head         int // chars kept from an elided result
}

// HistoryDietStats accumulates how much the diet removed, per session key.
type HistoryDietStats struct {
	Calls           int64 `json:"calls"`
	OriginalChars   int64 `json:"original_chars"`
	DietedChars     int64 `json:"dieted_chars"`
	ElidedResults   int64 `json:"elided_results"`
	DroppedThinking int64 `json:"dropped_thinking"`
	Rewrites        int64 `json:"rewrites"`
}

// DietFreezeState is a session's frozen boundary. Below FrozenLen the wire
// copy applies the frozen rule (elide + drop thinking) deterministically; the
// counts are best-effort compaction detection — compaction rewrites history
// in place, and a reset just re-freezes on the next trigger.
type DietFreezeState struct {
	FrozenLen     int
	FrozenResults int
	FrozenUser    int
	FrozenAsst    int
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
	cfg := HistoryDietConfig{
		Enabled:      dietEnvOn("ARTEX_HISTORY_DIET"),
		DropThinking: dietEnvOn("ARTEX_HISTORY_DIET_DROP_THINKING"),
		Keep:         dietEnvInt("ARTEX_HISTORY_DIET_KEEP", 8),
		Head:         dietEnvInt("ARTEX_HISTORY_DIET_HEAD", 300),
	}
	if cfg.Keep < 1 {
		cfg.Keep = 1
	}
	if cfg.Head < 1 {
		cfg.Head = 1
	}
	return cfg
}

// DietSticky computes the wire copy of msgs under the freeze state st and cfg,
// advancing the freeze when the live zone crosses the rewrite threshold. It
// never mutates msgs: untouched messages are shared, shrunken ones are new
// values (copy-on-write on the slice).
func DietSticky(msgs []llm.Message, st DietFreezeState, cfg HistoryDietConfig) ([]llm.Message, DietFreezeState, HistoryDietStats) {
	var stats HistoryDietStats
	if !cfg.Enabled || len(msgs) < 2 {
		// Disabled: a pure passthrough — no freeze bookkeeping either, so a
		// later enablement starts from a clean boundary.
		return msgs, DietFreezeState{}, stats
	}
	// Compaction / shrink guards: a history that no longer matches the frozen
	// prefix resets the boundary (the transform stays valid either way — the
	// frozen rule is stateless — but a stale boundary would mis-place the
	// freeze and break the cache for nothing).
	if st.FrozenLen > len(msgs) || st.FrozenLen < 0 || st.FrozenResults < 0 {
		st = DietFreezeState{}
	} else if st.FrozenLen > 0 {
		if countDietResults(msgs[:st.FrozenLen]) != st.FrozenResults ||
			countDietRoles(msgs[:st.FrozenLen]) != [2]int{st.FrozenUser, st.FrozenAsst} {
			st = DietFreezeState{}
		}
	}

	// Freeze trigger: the live zone has grown past the rewrite budget and
	// holds more than KEEP results.
	type pos struct{ mi, bi int }
	var all []pos
	for i := range msgs {
		for j := range msgs[i].Content {
			if msgs[i].Content[j].Type == llm.BlockToolResult {
				all = append(all, pos{i, j})
			}
		}
	}
	liveResults := len(all) - st.FrozenResults
	liveChars := 0
	for i := st.FrozenLen; i < len(msgs); i++ {
		liveChars += dietMessageChars(msgs[i])
	}
	// Freeze trigger — count-based hysteresis. A freeze runs when the live
	// zone holds more than 2*KEEP results; after it, exactly the newest KEEP
	// results (plus their turns) stay live. Between freezes at least KEEP new
	// results accumulate append-only, so the prefix cache survives the whole
	// stretch and pays exactly one break per freeze. (A char-based trigger
	// measured the RAW live zone while the wire live zone after a freeze is
	// smaller — the mismatch re-triggered a freeze every turn.)
	if liveResults > 2*cfg.Keep && len(all) >= cfg.Keep {
		boundary := all[len(all)-cfg.Keep].mi
		// The boundary never lands on the final message (the current turn and
		// the newest KEEP results stay verbatim).
		if boundary >= len(msgs)-1 {
			boundary = len(msgs) - 1
		}
		if boundary > st.FrozenLen {
			st.FrozenLen = boundary
			st.FrozenResults = countDietResults(msgs[:boundary])
			st.FrozenUser, st.FrozenAsst = countDietRoles(msgs[:boundary])[0], countDietRoles(msgs[:boundary])[1]
			stats.Rewrites++
		}
	}

	// Wire copy: the frozen rule below the boundary, verbatim above it.
	out := msgs
	cloned := false
	for i := 0; i < st.FrozenLen; i++ {
		var changed bool
		content := make([]llm.ContentBlock, 0, len(msgs[i].Content))
		for j := range msgs[i].Content {
			b := msgs[i].Content[j]
			switch {
			case b.Type == llm.BlockThinking && cfg.DropThinking:
				stats.DroppedThinking++
				changed = true
				continue // dropped from the wire copy
			case b.Type == llm.BlockToolResult:
				text := flattenToolResultText(b)
				if len(text) > cfg.Head {
					note := "\n…[history diet: " + strconv.Itoa(len(text)-cfg.Head) + " chars elided; re-run the tool if you need the full output]"
					nb := b
					nb.Content = []llm.ContentBlock{llm.TextBlock(text[:cfg.Head] + note)}
					stats.ElidedResults++
					content = append(content, nb)
					changed = true
					continue
				}
			}
			content = append(content, b)
		}
		if changed {
			if !cloned {
				out = append([]llm.Message(nil), msgs...)
				cloned = true
			}
			stats.OriginalChars += int64(dietMessageChars(msgs[i]))
			out[i] = llm.Message{Role: msgs[i].Role, Content: content}
			stats.DietedChars += int64(dietMessageChars(out[i]))
		}
	}
	return out, st, stats
}

func countDietResults(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult {
				n++
			}
		}
	}
	return n
}

func countDietRoles(msgs []llm.Message) [2]int {
	var r [2]int
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			r[0]++
		case llm.RoleAssistant:
			r[1]++
		}
	}
	return r
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
	historyDietMu  sync.Mutex
	historyDietAgg = map[string]*HistoryDietStats{}
	dietFreezes    sync.Map // session key -> *DietFreezeState
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
	key := transcript.SessionIDFrom(ctx)
	var st DietFreezeState
	if v, ok := dietFreezes.Load(key); ok {
		if s, ok2 := v.(*DietFreezeState); ok2 {
			st = *s
		}
	}
	out, nst, stats := DietSticky(req.Messages, st, cfg)
	if nst != st {
		dietFreezes.Store(key, &nst)
	}
	if stats.ElidedResults == 0 && stats.DroppedThinking == 0 {
		return req
	}
	historyDietMu.Lock()
	c := historyDietAgg[key]
	if c == nil {
		c = &HistoryDietStats{}
		historyDietAgg[key] = c
	}
	c.Calls++
	c.OriginalChars += stats.OriginalChars
	c.DietedChars += stats.DietedChars
	c.ElidedResults += stats.ElidedResults
	c.DroppedThinking += stats.DroppedThinking
	c.Rewrites += stats.Rewrites
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
