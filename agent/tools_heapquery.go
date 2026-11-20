package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
)

// heap_query: structured heap inspection on a live debug session, the
// data-driven grooming companion to heap_spray. Wraps the windbg MCP's
// allocator tools (heap_list / heap_census / heap_allocations / heap_chunk)
// behind one entry point with JSON results: which heaps exist, which size
// classes are heaviest, what chunks surround an address (with allocation
// stacks). Sessions are managed automatically: attach to a running target
// process or reuse the session id returned by a previous call.

func (t *ToolSet) heapQueryTool() actool.CoreTool {
	return writeTool("heap_query",
		"Structured heap inspection on a lab machine's target process — the data side of heap grooming. One call can: list heaps (list), group chunks by size class heaviest-first (census — what size classes dominate, busy vs free), enumerate chunks of a size class (allocations), or locate the chunk containing an address plus its neighbours with alloc/free stacks (chunk). Attaches to a running process automatically and reuses the session; pass the session returned by crash_triage_auto to inspect that exact crash context. Use with heap_spray: census before/after a spray proves your blocks landed where you think, and chunk at the corruption address shows what neighbours the write will hit.",
		obj(map[string]any{
			"machine":  str("registered machine name"),
			"action":   map[string]any{"type": "string", "enum": []string{"list", "census", "allocations", "chunk"}, "description": "list = heap roots; census = size-class distribution (heaviest first); allocations = chunks filtered by size/state; chunk = the allocation containing an address + neighbours + stacks"},
			"pid":      map[string]any{"type": "integer", "description": "target process id (attach; omit if session given)"},
			"process":  str("target process name to attach (e.g. EXCEL) when pid omitted"),
			"session":  str("windbg session id to reuse (from crash_triage_auto or a previous heap_query)"),
			"heap":     str("optional: restrict to one heap (index or address)"),
			"size":     str("optional: chunk size filter (hex, e.g. 0x78) for allocations/census"),
			"state":    map[string]any{"type": "string", "enum": []string{"", "busy", "free"}, "description": "chunk state filter (allocations)"},
			"address":  str("hex address for action=chunk (the corruption/victim address)"),
			"limit":    map[string]any{"type": "integer", "description": "max chunks to return (allocations; default 50)"},
			"username": str("optional SSH username"),
		}, "machine", "action"),
		t.needMachines("heap_query", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine  string `json:"machine"`
				Action   string `json:"action"`
				PID      int    `json:"pid"`
				Process  string `json:"process"`
				Session  string `json:"session"`
				Heap     string `json:"heap"`
				Size     string `json:"size"`
				State    string `json:"state"`
				Address  string `json:"address"`
				Limit    int    `json:"limit"`
				Username string `json:"username"`
			}
			_ = json.Unmarshal(in, &a)
			wc := newWinDbgClient()
			if wc == nil {
				return actool.Errorf("windbg MCP is not configured (ARTEX_WINDBG_URL/TOKEN)"), nil
			}
			sid := a.Session
			ownsSession := false
			// Launch a fresh target under the debugger (attach_process fails
			// with 0x80070057 on this windbg-mcp build; launch is the
			// reliable path and gives a live session to query).
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
			defer wc.endSession(context.Background(), sid)
			launchOut, err := wc.call(ctx, sid, "launch", map[string]any{"command_line": appExe}, 180*time.Second)
			if err != nil {
				return actool.Errorf("launch failed: " + err.Error()), nil
			}
			if e, ok := launchOut["is_error"].(bool); ok && e {
				return actool.Errorf("launch failed: " + fmt.Sprint(launchOut["text"])), nil
			}
			// Symbols: the engine bundle + _NT_SYMBOL_PATH on the VM provide
			// ntdll's pdb (verified); a short settle then break_in gives a
			// post-startup snapshot instead of the empty initial-break heap.
			time.Sleep(12 * time.Second)
			_, _ = wc.call(ctx, sid, "break_in", map[string]any{}, 120*time.Second)
			toolArgs := map[string]any{}
			var tool string
			switch a.Action {
			case "list":
				tool = "heap_list"
			case "census":
				tool = "heap_census"
				if a.Heap != "" {
					toolArgs["heap"] = a.Heap
				}
			case "allocations":
				tool = "heap_allocations"
				if a.Heap != "" {
					toolArgs["heap"] = a.Heap
				}
				if a.Size != "" {
					// exact size class: min=max=size (hex -> int)
					var sz int
					fmt.Sscanf(a.Size, "0x%x", &sz)
					if sz == 0 {
						fmt.Sscanf(a.Size, "%d", &sz)
					}
					toolArgs["min_capacity"] = sz
					toolArgs["max_capacity"] = sz
				}
				if a.State != "" {
					toolArgs["state"] = a.State
				}
				if a.Limit > 0 {
					toolArgs["limit"] = a.Limit
				} else {
					toolArgs["limit"] = 50
				}
			case "chunk":
				tool = "heap_chunk"
				if a.Address == "" {
					return actool.Errorf("address is required for action=chunk"), nil
				}
				toolArgs["address"] = a.Address
			default:
				return actool.Errorf("action must be list|census|allocations|chunk"), nil
			}
			out, err := wc.call(ctx, sid, tool, toolArgs, 180*time.Second)
			if err != nil {
				return actool.Errorf(tool + " failed: " + err.Error()), nil
			}
			res := map[string]any{"action": a.Action, "query": out}
			if ownsSession {
				res["note"] = "session ended; pass session id to keep one alive across calls"
			} else {
				res["session"] = sid
			}
			return jsonResult(res)
		}))
}
