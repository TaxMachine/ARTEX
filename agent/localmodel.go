package agent

import (
	"os"
	"strconv"
	"strings"
)

// Local-model context efficiency (e.g. a locally hosted Qwen 27B with a 262K
// window). Local inference degrades much faster than hosted APIs as the
// prefilled context grows: no massive datacenter KV-cache, and mid-size models
// lose instruction fidelity over very long transcripts. Three levers, each env
// tunable so hosted-model behavior stays byte-identical when unset:
//
//   - ARTEX_TOOL_OUT_CHARS   per-tool output cap (chars). Default 30000 is fine
//     for hosted frontier models; 8000 keeps transcripts lean locally. Spill
//     files keep the full output on disk, so nothing is lost.
//   - ARTEX_COMPACTION_K     compaction context window in K tokens. Default is
//     the profile's window (200K assumed); set ~60-80 for a local model so the
//     summarizer runs early and often, keeping live transcripts short.
//   - ARTEX_WORKER_GLOBAL_TOOLS  comma list of tool names to always keep in the
//     worker's schema despite tool-RAG projection (escape hatch; rarely needed).

// toolOutputChars resolves the per-tool output cap. 0 = SDK default (30000).
func toolOutputChars() int {
	if v := strings.TrimSpace(os.Getenv("ARTEX_TOOL_OUT_CHARS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 2000 && n <= 60000 {
			return n
		}
	}
	return 0
}

// localCompactionWindowK resolves a compaction window override in K tokens.
// 0 = keep the profile-derived window.
func localCompactionWindowK() int {
	if v := strings.TrimSpace(os.Getenv("ARTEX_COMPACTION_K")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 16 && n <= 1000 {
			return n
		}
	}
	return 0
}

// workerKeepTools lists tool names tool-RAG must never project away.
func workerKeepTools() map[string]bool {
	v := strings.TrimSpace(os.Getenv("ARTEX_WORKER_GLOBAL_TOOLS"))
	if v == "" {
		return nil
	}
	out := map[string]bool{}
	for _, t := range strings.Split(v, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out[t] = true
		}
	}
	return out
}

// LocalModelEfficiencyOn reports whether any local-efficiency lever is set
// (used by logging to make the active profile visible).
func LocalModelEfficiencyOn() bool {
	return toolOutputChars() > 0 || localCompactionWindowK() > 0
}

// toolDescMax resolves the per-tool description/parameter-description cap for
// schema slimming (ARTEX_TOOL_DESC_MAX chars; 0 = no slimming). The full text
// stays authoritative in the tools table — this only bounds what the model sees
// per call. Mid-size local models keep most tool-use fidelity from the first
// sentences; the long embedded tutorials mainly help frontier models.
func toolDescMax() int {
	if v := strings.TrimSpace(os.Getenv("ARTEX_TOOL_DESC_MAX")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 200 && n <= 8000 {
			return n
		}
	}
	return 0
}
