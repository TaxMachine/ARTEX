package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
)

// PageHeap / heap-diagnostics tooling for exploit PoC work: enable full page
// heap on a target process (allocator faults AT the corruption instead of at
// crash-time), toggle silent vs full mode, and drive the heap spray harness on
// the lab VM. Registry-based (what gflags does under the hood) so no debugging
// tools are needed on the guest.

func (t *ToolSet) pageHeapTool() actool.CoreTool {
	return writeTool("page_heap",
		"Control full page heap for a Windows process on a lab machine — THE corruption-precision tool for heap-grooming PoCs. enable=full: every allocation gets guard pages; a heap overflow faults AT the moment of corruption (first-chance, exact instruction) instead of crashing three-hundred allocations later. enable=silent: page heap bookkeeping without the immediate fault (catches corruption on free, keeps execution going — use when full-page kills the process before the groom completes). disable: remove. status: read current settings. Requires a registered Windows machine with an SSH credential; the process must be (re)started after enabling for the setting to take effect.",
		obj(map[string]any{
			"machine":  str("registered machine name"),
			"process":  str("process executable name, e.g. WINWORD.EXE"),
			"action":   map[string]any{"type": "string", "enum": []string{"enable", "disable", "status"}, "description": "enable (full by default), disable, or status"},
			"mode":     map[string]any{"type": "string", "enum": []string{"full", "silent"}, "description": "enable mode: full (default) = fault at corruption; silent = detect on free, process keeps running"},
			"username": str("optional SSH username"),
		}, "machine", "process", "action"),
		t.needMachines("page_heap", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine  string `json:"machine"`
				Process  string `json:"process"`
				Action   string `json:"action"`
				Mode     string `json:"mode"`
				Username string `json:"username"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Process == "" || a.Action == "" {
				return actool.Errorf("process and action are required"), nil
			}
			proc := sanitizeProcName(a.Process)
			if proc == "" {
				return actool.Errorf("invalid process name"), nil
			}
			key := `HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Image File Execution Options\` + proc
			mode := a.Mode
			if mode == "" {
				mode = "full"
			}
			var ps string
			switch a.Action {
			case "status":
				ps = fmt.Sprintf(`$k='%s'; if (Test-Path $k) { $g=(Get-ItemProperty $k -ErrorAction SilentlyContinue).GlobalFlag; $p=(Get-ItemProperty $k -ErrorAction SilentlyContinue).PageHeapFlags; Write-Output ('GlobalFlag=' + $g + ' PageHeapFlags=' + $p) } else { Write-Output 'NOT_CONFIGURED' }`, key)
			case "enable":
				// GlobalFlag 0x02000000 = heap tail checking + page heap;
				// PageHeapFlags: 0x01 = full page heap, 0x02 = silent (fault on free)
				flags := "0x3" // full: page heap + stack traces + fault at corruption (0x01|0x02 per gflags /full)
				if mode == "silent" {
					flags = "0x2" // silent: detect on free
				}
				ps = fmt.Sprintf(`$k='%s'; New-Item -Path $k -Force | Out-Null; Set-ItemProperty -Path $k -Name GlobalFlag -Value 0x02000000 -Type DWord; Set-ItemProperty -Path $k -Name PageHeapFlags -Value %s -Type DWord; Write-Output ('PAGEHEAP_ENABLED mode=%s ' + (Get-ItemProperty $k).PageHeapFlags)`, key, flags, mode)
			case "disable":
				ps = fmt.Sprintf(`$k='%s'; if (Test-Path $k) { Remove-ItemProperty -Path $k -Name GlobalFlag -ErrorAction SilentlyContinue; Remove-ItemProperty -Path $k -Name PageHeapFlags -ErrorAction SilentlyContinue; Write-Output 'PAGEHEAP_DISABLED' } else { Write-Output 'NOT_CONFIGURED' }`, key)
			default:
				return actool.Errorf("action must be enable|disable|status"), nil
			}
			out, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
				"powershell -NoProfile -Command \""+strings.ReplaceAll(ps, `"`, `""`)+`"`, 30*time.Second)
			if err != nil {
				return actool.Errorf("page_heap failed: " + err.Error()), nil
			}
			return jsonResult(map[string]any{"process": proc, "action": a.Action, "mode": mode, "output": out.Stdout, "exit_code": out.ExitCode})
		}))
}

func sanitizeProcName(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if !strings.Contains(strings.ToLower(out), ".exe") {
		out += ".exe"
	}
	if len(out) > 4 && strings.ToLower(out[len(out)-4:]) == ".exe" && strings.Count(strings.ToLower(out), ".exe") == 1 {
		return out
	}
	return ""
}

// heapSprayTool drives the VM-side spray harness: allocate/free patterns of
// controlled-size allocations (BSTR-style, COM, raw VirtualAlloc) so a PoC can
// position the heap deterministically before triggering the vulnerable path.
func (t *ToolSet) heapSprayTool() actool.CoreTool {
	return writeTool("heap_spray",
		"Drive deterministic heap grooming on a lab Windows machine: allocate N blocks of a given size with a chosen filler pattern (spray), free them all (clear), or report the harness state. The harness runs as a background PowerShell process on the VM holding the allocations alive for the session — spray first, then trigger the vulnerable path from the same task (ssh_exec), then clear. Pairs with page_heap: page_heap=full makes the overflow fault exactly where it lands, heap_spray positions the heap so it lands where the chain needs it.",
		obj(map[string]any{
			"machine":  str("registered machine name"),
			"action":   map[string]any{"type": "string", "enum": []string{"spray", "clear", "status"}, "description": "spray = allocate the pattern; clear = free everything the harness holds; status = report held blocks"},
			"count":    map[string]any{"type": "integer", "description": "number of blocks to allocate (spray)"},
			"size":     map[string]any{"type": "integer", "description": "block size in bytes (spray); pick the victim allocator's size class"},
			"filler":   str("optional filler pattern for the blocks (default 0x41 repeated)"),
			"username": str("optional SSH username"),
		}, "machine", "action"),
		t.needMachines("heap_spray", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine  string `json:"machine"`
				Action   string `json:"action"`
				Count    int    `json:"count"`
				Size     int    `json:"size"`
				Filler   string `json:"filler"`
				Username string `json:"username"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Action == "" {
				return actool.Errorf("action is required"), nil
			}
			var ps string
			switch a.Action {
			case "status":
				ps = `if (Test-Path "$env:TEMP\artex-spray.json") { Get-Content "$env:TEMP\artex-spray.json" } else { Write-Output 'HARNESS_IDLE' }`
			case "clear":
				ps = `Get-Process artex-spray -ErrorAction SilentlyContinue | Stop-Process -Force; Remove-Item "$env:TEMP\artex-spray.json" -ErrorAction SilentlyContinue; Write-Output 'SPRAY_CLEARED'`
			case "spray":
				if a.Count <= 0 || a.Count > 100000 || a.Size < 16 || a.Size > 0x1000000 {
					return actool.Errorf("count must be 1-100000 and size 16-16M bytes"), nil
				}
				filler := sanitizeSprayFiller(a.Filler)
				if len(a.Filler) > 64 {
					return actool.Errorf("filler pattern too long (max 64 chars)"), nil
				}
				// The harness script with the parameters INLINED (the guest's
				// execution policy blocks -File scripts, and SSH quoting mangles
				// argument lists — an EncodedCommand with everything baked in is
				// immune to both). The detached process holds the allocations
				// alive and reports its state to a JSON file.
				harnessBody := `$blocks = New-Object System.Collections.ArrayList
$pattern = [Text.Encoding]::UTF8.GetBytes('%s')
$b = New-Object byte[] %d
for ($i = 0; $i -lt $b.Length; $i++) { $b[$i] = $pattern[$i %% $pattern.Length] }
for ($i = 0; $i -lt %d; $i++) { [void]$blocks.Add($b.Clone()) }
$info = @{ count = %d; size = %d; pid = $PID; filler = '%s'; at = (Get-Date -Format s) } | ConvertTo-Json -Compress
Set-Content -Path "$env:TEMP\artex-spray.json" -Value $info
while ($true) { Start-Sleep -Seconds 60 }`
				// one command: start the detached harness (EncodedCommand with
				// the parameters inlined — immune to both the execution policy
				// and the SSH quoting gauntlet), then read the state file.
				script := fmt.Sprintf(harnessBody, filler, a.Size, a.Count, a.Count, a.Size, filler)
				ps = fmt.Sprintf(`Remove-Item "$env:TEMP\artex-spray.json" -ErrorAction SilentlyContinue; Start-Process powershell -WindowStyle Hidden -ArgumentList @('-NoProfile','-EncodedCommand','%s'); Start-Sleep -Seconds 5; if (Test-Path "$env:TEMP\artex-spray.json") { Get-Content "$env:TEMP\artex-spray.json" } else { Write-Output 'SPRAY_FAILED' }`,
					psEncode64(script))
			default:
				return actool.Errorf("action must be spray|clear|status"), nil
			}
			out, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
				"powershell -NoProfile -EncodedCommand "+psEncode64(ps), 60*time.Second)
			if err != nil {
				return actool.Errorf("heap_spray failed: " + err.Error()), nil
			}
			return jsonResult(map[string]any{"action": a.Action, "output": out.Stdout, "exit_code": out.ExitCode})
		}))
}

// psEncode64 encodes a PowerShell script for -EncodedCommand (UTF-16LE base64).
func psEncode64(script string) string {
	u16 := make([]byte, 0, len(script)*2)
	for _, b := range []byte(script) {
		u16 = append(u16, b, 0)
	}
	const b64chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	// standard base64
	return base64Std(u16)
}

func base64Std(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func sanitizeSprayFiller(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "A"
	}
	return b.String()
}
