package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
)

// heap_watch: census deltas around a groom/trigger — what size classes churned
// between two moments. The agent snapshots (start), performs the spray/trigger
// between calls, then diffs. State is in-process per task+machine+heap.

type heapWatchKey struct {
	taskID  int64
	machine string
}

type heapWatchSnap struct {
	rows    []map[string]any
	session string
	at      time.Time
}

var (
	heapWatchMu    sync.Mutex
	heapWatchSnaps = map[heapWatchKey]*heapWatchSnap{}
)

func (t *ToolSet) heapWatchTool() actool.CoreTool {
	return writeTool("heap_watch",
		"Measure what the heap did between two moments — the groom feedback loop. action=snapshot: census now (kept as the baseline; pass the session from a running heap_query/crash_triage_auto to census THAT target, else a fresh one launches). action=diff: census again on the same target and return the DELTA — size classes that grew/shrank, new/free'd chunks — so you see exactly what a heap_spray or a document-open churned. Typical loop: snapshot -> heap_spray -> diff (did the spray land in the victim's class?) -> adjust -> snapshot -> trigger -> diff (did the victim allocate where expected?).",
		obj(map[string]any{
			"machine":  str("registered machine name"),
			"action":   map[string]any{"type": "string", "enum": []string{"snapshot", "diff"}, "description": "snapshot = baseline census now; diff = re-census + delta vs the snapshot"},
			"session":  str("windbg session id to census (from heap_query/crash_triage_auto); omit to launch a fresh target"),
			"process":  str("target for a fresh launch (default EXCEL) when no session"),
			"limit":    map[string]any{"type": "integer", "description": "max delta rows to return (default 15) — largest absolute changes first"},
			"username": str("optional SSH username"),
		}, "machine", "action"),
		t.needMachines("heap_watch", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine  string `json:"machine"`
				Action   string `json:"action"`
				Session  string `json:"session"`
				Process  string `json:"process"`
				Limit    int    `json:"limit"`
				Username string `json:"username"`
			}
			_ = json.Unmarshal(in, &a)
			key := heapWatchKey{taskID: t.taskID, machine: a.Machine}
			wc := newWinDbgClient()
			if wc == nil {
				return actool.Errorf("windbg MCP is not configured (ARTEX_WINDBG_URL/TOKEN)"), nil
			}
			sid := a.Session
			ownsSession := false
			if sid == "" {
				// reuse the snapshot's session on diff; fresh launch otherwise
				heapWatchMu.Lock()
				snap := heapWatchSnaps[key]
				heapWatchMu.Unlock()
				if a.Action == "diff" && snap != nil && snap.session != "" {
					sid = snap.session
				} else {
					app := a.Process
					if app == "" {
						app = "EXCEL"
					}
					appExe := map[string]string{
						"WINWORD": `C:\Program Files\Microsoft Office\root\Office16\WINWORD.EXE`,
						"EXCEL":   `C:\Program Files\Microsoft Office\root\Office16\EXCEL.EXE`,
					}[app]
					if appExe == "" {
						appExe = app
					}
					var err error
					sid, err = wc.initialize(ctx)
					if err != nil {
						return actool.Errorf("windbg initialize failed: " + err.Error()), nil
					}
					ownsSession = true
					launchOut, err := wc.call(ctx, sid, "launch", map[string]any{"command_line": appExe}, 180*time.Second)
					if err == nil {
						if e, ok := launchOut["is_error"].(bool); ok && e {
							return actool.Errorf("launch failed: " + fmt.Sprint(launchOut["text"])), nil
						}
					}
					time.Sleep(12 * time.Second)
					_, _ = wc.call(ctx, sid, "break_in", map[string]any{}, 120*time.Second)
				}
			}
			census, err := wc.call(ctx, sid, "heap_census", map[string]any{"limit": 2000}, 300*time.Second)
			if err != nil {
				return actool.Errorf("census failed: " + err.Error()), nil
			}
			rows := censusRows(census)

			_ = ownsSession // sessions persist between snapshot/diff on purpose
			switch a.Action {
			case "snapshot":
				heapWatchMu.Lock()
				heapWatchSnaps[key] = &heapWatchSnap{rows: rows, session: sid, at: time.Now()}
				heapWatchMu.Unlock()
				return jsonResult(map[string]any{
					"action": "snapshot", "session": sid, "heap_groups": len(rows),
					"at":   time.Now().Format(time.RFC3339),
					"note": "baseline stored; run the spray/trigger, then heap_watch action=diff",
				})
			case "diff":
				heapWatchMu.Lock()
				snap := heapWatchSnaps[key]
				heapWatchMu.Unlock()
				if snap == nil {
					return actool.Errorf("no snapshot for this task+machine — run action=snapshot first"), nil
				}
				limit := a.Limit
				if limit <= 0 {
					limit = 15
				}
				delta := censusDelta(snap.rows, rows, limit)
				heapWatchMu.Lock()
				heapWatchSnaps[key] = &heapWatchSnap{rows: rows, session: sid, at: time.Now()}
				heapWatchMu.Unlock()
				return jsonResult(map[string]any{
					"action": "diff", "session": sid,
					"baseline_at": snap.at.Format(time.RFC3339),
					"changes":     delta,
					"note":        "positive chunks = grew (allocations added), negative = shrank (frees); focus the classes your spray targets",
				})
			}
			return actool.Errorf("action must be snapshot|diff"), nil
		}))
}

// censusRows extracts the rows array from a heap_census result (structured
// preferred, text fallback).
func censusRows(res map[string]any) []map[string]any {
	if sc, ok := res["structuredContent"].(map[string]any); ok {
		if rows, ok := sc["rows"].([]any); ok {
			out := make([]map[string]any, 0, len(rows))
			for _, r := range rows {
				if m, ok := r.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out
		}
	}
	return nil
}

type classKey struct {
	heap    string
	cls     int
	state   string
	backend string
}

func rowKey(r map[string]any) classKey {
	return classKey{
		heap:    fmt.Sprint(r["heap"]),
		cls:     int(toFloat(r["size_class"])),
		state:   fmt.Sprint(r["state"]),
		backend: fmt.Sprint(r["backend"]),
	}
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

// censusDelta diffs two census row sets keyed by heap+size_class+state and
// returns the largest absolute chunk-count changes.
func censusDelta(before, after []map[string]any, limit int) []map[string]any {
	bm := map[classKey]map[string]any{}
	for _, r := range before {
		bm[rowKey(r)] = r
	}
	am := map[classKey]map[string]any{}
	for _, r := range after {
		am[rowKey(r)] = r
	}
	type ch struct {
		key  classKey
		d    int
		b, a int
		capD int
	}
	var out []ch
	for k, ar := range am {
		aChunks := int(toFloat(ar["chunks"]))
		aCap := int(toFloat(ar["total_capacity"]))
		if br, ok := bm[k]; ok {
			bChunks := int(toFloat(br["chunks"]))
			if aChunks != bChunks {
				out = append(out, ch{k, aChunks - bChunks, bChunks, aChunks, aCap - int(toFloat(br["total_capacity"]))})
			}
		} else {
			out = append(out, ch{k, aChunks, 0, aChunks, aCap})
		}
	}
	for k, br := range bm {
		if _, ok := am[k]; !ok {
			out = append(out, ch{k, -int(toFloat(br["chunks"])), int(toFloat(br["chunks"])), 0, -int(toFloat(br["total_capacity"]))})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return abs(out[i].d) > abs(out[j].d)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	res := make([]map[string]any, 0, len(out))
	for _, c := range out {
		res = append(res, map[string]any{
			"heap": c.key.heap, "size_class": c.key.cls, "state": c.key.state, "backend": c.key.backend,
			"chunks_before": c.b, "chunks_after": c.a, "chunks_delta": c.d, "capacity_delta": c.capD,
		})
	}
	return res
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
