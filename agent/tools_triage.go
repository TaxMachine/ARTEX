package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
)

// crash_triage_auto: the fully automated fuzz-crash -> primitive pipeline.
// Stages the crashing input on the lab VM, enables full page heap on the
// target, launches the target under windbg (via the windbg MCP) with the file,
// waits for the crash, runs crash_triage (!analyze), and returns a STRUCTURED
// primitive record (fault code, faulting instruction/module+offset, stack,
// crossmap symbolization when indexed) ready to record as a fact.

func (t *ToolSet) crashTriageAutoTool() actool.CoreTool {
	return writeTool("crash_triage_auto",
		"Automated crash triage for a crashing input found by fuzz_campaign: one call runs the whole pipeline — enables full page heap on the target, launches it under the debugger (windbg) opening the crashing file, waits for the crash, runs !analyze, and returns a structured primitive record: exception code, faulting module+offset+instruction, stack frames, and the function name via crossmap when the module is indexed. Record the returned primitive as a fact (it is a PRIMITIVE, not a finding) and let the planner dispatch grooming. The input stays on the VM at its path; page_heap is disabled again afterwards unless keep_page_heap=true.",
		obj(map[string]any{
			"machine":        str("registered machine name"),
			"crash_file":     str("VM path of the crashing input (from fuzz_campaign action=crashes)"),
			"target":         map[string]any{"type": "string", "enum": []string{"WINWORD", "EXCEL"}, "description": "target application"},
			"frames":         map[string]any{"type": "integer", "description": "stack frames to walk (default 24)"},
			"keep_page_heap": map[string]any{"type": "boolean", "description": "leave page heap enabled after triage (default false)"},
			"username":       str("optional SSH username"),
		}, "machine", "crash_file", "target"),
		t.needMachines("crash_triage_auto", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine      string `json:"machine"`
				CrashFile    string `json:"crash_file"`
				Target       string `json:"target"`
				Frames       int    `json:"frames"`
				KeepPageHeap bool   `json:"keep_page_heap"`
				Username     string `json:"username"`
			}
			_ = json.Unmarshal(in, &a)
			if a.CrashFile == "" || (a.Target != "WINWORD" && a.Target != "EXCEL") {
				return actool.Errorf("crash_file and target (WINWORD|EXCEL) are required"), nil
			}
			if !safeVMPath(a.CrashFile) {
				return actool.Errorf("crash_file must be an absolute VM path"), nil
			}
			frames := a.Frames
			if frames <= 0 {
				frames = 24
			}
			appExe := map[string]string{"WINWORD": `C:\Program Files\Microsoft Office\root\Office16\WINWORD.EXE`, "EXCEL": `C:\Program Files\Microsoft Office\root\Office16\EXCEL.EXE`}[a.Target]

			// 1) page heap on (full) for the target
			enablePS := fmt.Sprintf(`$k='HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Image File Execution Options\%s.EXE'; New-Item -Path $k -Force | Out-Null; Set-ItemProperty -Path $k -Name GlobalFlag -Value 0x02000000 -Type DWord; Set-ItemProperty -Path $k -Name PageHeapFlags -Value 0x3 -Type DWord; Write-Output PH_ON`, a.Target)
			if out, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
				"powershell -NoProfile -EncodedCommand "+psEncode64(enablePS), 20*time.Second); err != nil || !strings.Contains(out.Stdout, "PH_ON") {
				return actool.Errorf("page_heap enable failed: " + out.Stdout + " " + errText(err)), nil
			}
			if !a.KeepPageHeap {
				defer func() {
					disPS := fmt.Sprintf(`$k='HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Image File Execution Options\%s.EXE'; Remove-ItemProperty -Path $k -Name GlobalFlag,PageHeapFlags -ErrorAction SilentlyContinue; Write-Output PH_OFF`, a.Target)
					_, _ = t.machines.Exec(context.Background(), t.taskID, a.Machine, a.Username, 0,
						"powershell -NoProfile -EncodedCommand "+psEncode64(disPS), 20*time.Second)
				}()
			}

			// 2) launch the target under windbg via the MCP bridge. The windbg
			// MCP runs ON the VM (127.0.0.1:8765); from here we reach it through
			// the MCP tools if wired (mcp__windbg__*), else instruct manually.
			// The launch command opens the crashing file through the app's own
			// command line (/q /n /e for headless open).
			var cmdLine string
			if a.Target == "WINWORD" {
				cmdLine = fmt.Sprintf(`%s /q /n "%s"`, appExe, a.CrashFile)
			} else {
				cmdLine = fmt.Sprintf(`%s /e "%s"`, appExe, a.CrashFile)
			}
			// 2) drive the windbg MCP over HTTP (the same server the
			// mcp__windbg__ tools use; agent tools can't call MCP tools
			// in-process). Sessions are short-lived and sequential.
			wc := newWinDbgClient()
			if wc == nil {
				return actool.Errorf("windbg MCP is not configured on this server (set ARTEX_WINDBG_URL + ARTEX_WINDBG_TOKEN)"), nil
			}
			sidStr, err := wc.initialize(ctx)
			if err != nil {
				return actool.Errorf("windbg initialize failed: " + err.Error()), nil
			}
			sid := &sidStr
			defer wc.endSession(context.Background(), *sid)
			// 3) launch; continue past the debugger's initial breakpoint, then
			// wait for the actual crash. Each continue is execute g — on a
			// page-heaped target the next stop IS the corruption.
			launchOut, err := wc.call(ctx, sid, "launch", map[string]any{"command_line": cmdLine}, 180*time.Second)
			if err != nil {
				return actool.Errorf("windbg launch failed: " + err.Error()), nil
			}
			// 4) run to the crash: continue; skip initial-break noise.
			var stopOut map[string]any
			stopIsInitial := func(m map[string]any) bool {
				t := fmt.Sprint(m["text"])
				return strings.Contains(t, "initial") || (strings.Contains(t, "Breakpoint A breakpoint") && strings.Contains(t, "first chance"))
			}
			for attempt := 0; attempt < 4; attempt++ {
				stopOut, err = wc.call(ctx, sid, "execute", map[string]any{"command": "g"}, 240*time.Second)
				if err != nil || !stopIsInitial(stopOut) {
					break
				}
			}
			// 5) triage the stop we settled on
			triageOut, err := wc.call(ctx, sid, "exception_triage", map[string]any{"analyze": true, "frames": frames}, 240*time.Second)
			if err != nil {
				return jsonResult(map[string]any{"stage": "triage", "launch": launchOut, "stop": stopOut, "error": err.Error()})
			}
			// 6) symbolize: extract module+offset from the triage output.
			symbolized := crossmapSymbolize(triageOut)
			return jsonResult(map[string]any{
				"action": "crash_triage_auto", "machine": a.Machine, "target": a.Target,
				"crash_file": a.CrashFile, "command_line": cmdLine,
				"launch": launchOut, "stop": stopOut, "triage": triageOut, "symbol": symbolized,
				"note": "record this as a PRIMITIVE fact (not a finding); compose chains per the confirmation standard",
			})
		}))
}

func safeVMPath(p string) bool {
	if !strings.HasPrefix(p, `C:\`) && !strings.HasPrefix(p, `c:\`) {
		return false
	}
	if strings.ContainsAny(p, "\x00\"|>&") {
		return false
	}
	return len(p) < 240
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
