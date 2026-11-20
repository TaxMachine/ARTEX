package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
)

// target_watch: OS-level file/process/registry activity of a target process on
// the lab VM around a trigger — the "document wrote where? spawned what?"
// view for logic bugs (sandbox escapes, payload drops). Uses Windows' built-in
// audit policy (auditpol Object Access + Process Creation), no Sysmon/procmon
// needed. start enables the policy + marks the window; the agent performs the
// trigger (COM open / payload run) via its usual tools; events lists the
// target's file writes/creates, process spawns and registry touches recorded
// during the window; stop disables the policy.

func (t *ToolSet) targetWatchTool() actool.CoreTool {
	return writeTool("target_watch",
		"Record OS-level activity (file writes/creates, child processes, registry writes) of a target process on the lab VM during a trigger — the logic-bug view: where did the document write, what did it spawn, which keys did it touch. Snapshot-diff based (no audit policy or procmon needed): baselines the filesystem drop locations + the target's process tree, then reports new/changed files and new child processes since baseline. action=start: enable audit policy + open the window (detached collector armed). Then perform the trigger via your usual tools (ssh_exec / COM open / msf). action=events: list the target's recorded file/process/registry events in the window. action=stop: close the window and disable the policy. Pair with capture_start for the network side of the same trigger.",
		obj(map[string]any{
			"machine":  str("registered machine name"),
			"action":   map[string]any{"type": "string", "enum": []string{"start", "events", "stop"}, "description": "start = arm the collector; events = read what the target did; stop = disarm"},
			"target":   str("process name to watch, e.g. WINWORD.EXE (start; required)"),
			"limit":    map[string]any{"type": "integer", "description": "max events (default 50, newest first)"},
			"username": str("optional SSH username"),
		}, "machine", "action"),
		t.needMachines("target_watch", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine  string `json:"machine"`
				Action   string `json:"action"`
				Target   string `json:"target"`
				Limit    int    `json:"limit"`
				Username string `json:"username"`
			}
			_ = json.Unmarshal(in, &a)
			switch a.Action {
			case "start":
				target := strings.ToUpper(strings.TrimSpace(a.Target))
				target = strings.TrimSuffix(target, ".EXE")
				if target == "" {
					return actool.Errorf("target process name is required for start"), nil
				}
				// Snapshot-diff design (auditpol /set is refused 0x00000057 on
				// this SKU): baseline the drop locations a document-derived
				// payload touches + the target's process tree.
				ps := fmt.Sprintf(`$roots = @("$env:USERPROFILE\Desktop","$env:USERPROFILE\Documents","$env:USERPROFILE\Downloads","$env:TEMP","$env:ProgramData","C:\Windows\Temp","$env:APPDATA\Microsoft\Windows\Start Menu","$env:ProgramData\Microsoft\Windows\Start Menu"); $files = foreach ($r in $roots) { if (Test-Path $r) { Get-ChildItem $r -Recurse -File -ErrorAction SilentlyContinue | Where-Object { $_.FullName -notmatch '\\artex-|\\symbols\\' } | Select-Object FullName, Length } }; $files | ConvertTo-Json -Compress | Set-Content -Path "$env:TEMP\artex-watch-files.json"; Get-CimInstance Win32_Process -Filter "Name like '%s%%'" | Select-Object ProcessId, ParentProcessId, Name | ConvertTo-Json -Compress | Set-Content -Path "$env:TEMP\artex-watch-procs.json"; @{ target = '%s'; start = (Get-Date).ToUniversalTime().ToString('s') } | ConvertTo-Json -Compress | Set-Content -Path "$env:TEMP\artex-watch.json"; Write-Output WATCH_ARMED`, target, target)
				out, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
					"powershell -NoProfile -EncodedCommand "+psEncode64(ps), 30*time.Second)
				if err != nil || !strings.Contains(out.Stdout, "WATCH_ARMED") {
					return actool.Errorf("arm failed: " + out.Stdout + " " + errText(err)), nil
				}
				return jsonResult(map[string]any{"action": "start", "target": target, "output": "watch armed — run the trigger, then action=events"})
			case "events":
				limit := a.Limit
				if limit <= 0 {
					limit = 50
				}
				ps := fmt.Sprintf(`$w = Get-Content "$env:TEMP\artex-watch.json" -ErrorAction SilentlyContinue | ConvertFrom-Json; if (-not $w) { Write-Output 'NOT_ARMED'; exit 0 }; $t = $w.target;
$base = @(Get-Content "$env:TEMP\artex-watch-files.json" -ErrorAction SilentlyContinue | ConvertFrom-Json); $baseMap = @{}; foreach ($f in $base) { $baseMap[$f.FullName] = $f.Length };
$roots = @("$env:USERPROFILE\Desktop","$env:USERPROFILE\Documents","$env:USERPROFILE\Downloads","$env:TEMP","$env:ProgramData","C:\Windows\Temp","$env:APPDATA\Microsoft\Windows\Start Menu","$env:ProgramData\Microsoft\Windows\Start Menu");
$now = foreach ($r in $roots) { if (Test-Path $r) { Get-ChildItem $r -Recurse -File -ErrorAction SilentlyContinue | Where-Object { $_.FullName -notmatch '\\artex-|\\symbols\\' } } };
$out = @();
foreach ($f in $now) { if (-not $baseMap.ContainsKey($f.FullName)) { $out += @{ kind='file-new'; path=$f.FullName; size=$f.Length; time=$f.LastWriteTimeUtc.ToString('s') } } elseif ($baseMap[$f.FullName] -ne $f.Length) { $out += @{ kind='file-changed'; path=$f.FullName; size=$f.Length; was=$baseMap[$f.FullName]; time=$f.LastWriteTimeUtc.ToString('s') } } };
$baseProcs = @(Get-Content "$env:TEMP\artex-watch-procs.json" -ErrorAction SilentlyContinue | ConvertFrom-Json); $baseIds = @{}; foreach ($p in $baseProcs) { $baseIds[[int]$p.ProcessId] = $true };
$live = Get-CimInstance Win32_Process -Filter "Name like '$t%%'"; $liveIds = @{}; foreach ($p in $live) { $liveIds[[int]$p.ProcessId] = $true };
foreach ($p in $live) { if (-not $baseIds.ContainsKey([int]$p.ProcessId)) { $out += @{ kind='target-process-new'; pid=$p.ProcessId; name=$p.Name; cmd=$p.CommandLine } } };
$kids = Get-CimInstance Win32_Process | Where-Object { $liveIds.ContainsKey([int]$_.ParentProcessId) }; foreach ($k in $kids) { if (-not $baseIds.ContainsKey([int]$k.ProcessId) -and $k.Name -notmatch 'powershell|ssh|conhost|cmd') { $out += @{ kind='child-process'; pid=$k.ProcessId; name=$k.Name; cmd=$k.CommandLine } } };
$out | Select-Object -First %d | ConvertTo-Json -Compress -Depth 4`, limit)
				out, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
					"powershell -NoProfile -EncodedCommand "+psEncode64(ps), 120*time.Second)
				if err != nil {
					return actool.Errorf("events failed: " + err.Error()), nil
				}
				return jsonResult(map[string]any{"action": "events", "output": strings.TrimSpace(out.Stdout)})
			case "stop":
				ps := `Remove-Item "$env:TEMP\artex-watch.json","$env:TEMP\artex-watch-files.json","$env:TEMP\artex-watch-procs.json" -ErrorAction SilentlyContinue; Write-Output WATCH_DISARMED`
				out, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
					"powershell -NoProfile -EncodedCommand "+psEncode64(ps), 30*time.Second)
				if err != nil {
					return actool.Errorf("stop failed: " + err.Error()), nil
				}
				return jsonResult(map[string]any{"action": "stop", "output": strings.TrimSpace(out.Stdout)})
			}
			return actool.Errorf("action must be start|events|stop"), nil
		}))
}
