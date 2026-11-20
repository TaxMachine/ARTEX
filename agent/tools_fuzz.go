package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
)

// fuzzScript is the VM-side mutation engine, deployed on action=deploy.
//
//go:embed assets/artex-fuzzer.ps1
var fuzzScript string

// fuzz_campaign: drive the VM-side structure-aware mutation fuzzer against an
// Office-class target. Deploys artex-fuzzer.ps1 to the VM on first use (or when
// the local copy is newer), runs detached campaigns, polls state, and reports
// unique crash signatures with their crashing inputs — the find-rate multiplier
// that turns agents from guessing where bugs are into triaging what the fuzzer
// found. Crashes should then be triaged: page_heap + open the crashing input
// under windbg (crash_triage) to identify the primitive, and record it.

func (t *ToolSet) fuzzCampaignTool() actool.CoreTool {
	return writeTool("fuzz_campaign",
		"Run a structure-aware file-mutation fuzzing campaign against Word/Excel on a lab Windows machine — THE bug-finder for document parsers. Mutates seed documents (byte flips, structure-block splice/duplicate/cut, OOXML-part-aware XML mutations incl. zip re-entry), opens each via headless COM, detects crashes via WER (event 1001), groups them by signature, and keeps one crashing input per unique signature. Deploy the fuzzer first (action=deploy), stage seeds via ssh_upload into the seeds dir, then start a campaign (action=start, detached — poll with action=status). On unique crashes: fetch the crash list (action=crashes), then triage each crashing input with page_heap + windbg crash_triage to identify the primitive and record it (primitives → facts; the chain remains the finding).",
		obj(map[string]any{
			"machine":  str("registered machine name"),
			"action":   map[string]any{"type": "string", "enum": []string{"deploy", "start", "status", "stop", "crashes"}, "description": "deploy = push the fuzzer script to the VM; start = launch a detached campaign; status = read state.json; stop = end early; crashes = list unique crash signatures + files"},
			"target":   map[string]any{"type": "string", "enum": []string{"WINWORD", "EXCEL"}, "description": "target application (start)"},
			"seeds":    str("VM directory with seed files (start; deploy creates C:\\fuzz\\seeds)"),
			"out":      str("VM output directory (start; default C:\\fuzz\\run-<timestamp>)"),
			"seconds":  map[string]any{"type": "integer", "description": "campaign duration, default 600, max 21600 (6h)"},
			"mode":     map[string]any{"type": "string", "enum": []string{"flip", "chunk", "xml", "all"}, "description": "mutation strategy mix (default all)"},
			"username": str("optional SSH username"),
		}, "machine", "action"),
		t.needMachines("fuzz_campaign", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine  string `json:"machine"`
				Action   string `json:"action"`
				Target   string `json:"target"`
				Seeds    string `json:"seeds"`
				Out      string `json:"out"`
				Seconds  int    `json:"seconds"`
				Mode     string `json:"mode"`
				Username string `json:"username"`
			}
			_ = json.Unmarshal(in, &a)
			const fuzzerPS = "C:\\fuzz\\artex-fuzzer.ps1"
			switch a.Action {
			case "deploy":
				// The script ships embedded in the binary and is written to the VM
				// (base64 -EncodedCommand free: write via a here-doc through powershell).
				ps := fmt.Sprintf(`New-Item -ItemType Directory -Force -Path C:\fuzz\seeds | Out-Null; [IO.File]::WriteAllBytes('%s', [Convert]::FromBase64String('%s')); Write-Output ('DEPLOYED ' + (Get-Item '%s').Length + ' bytes')`,
					fuzzerPS, b64(fuzzScript), fuzzerPS)
				out, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
					"powershell -NoProfile -EncodedCommand "+psEncode64(ps), 30*time.Second)
				if err != nil {
					return actool.Errorf("deploy failed: " + err.Error()), nil
				}
				return jsonResult(map[string]any{"action": "deploy", "output": out.Stdout})

			case "start":
				if a.Target != "WINWORD" && a.Target != "EXCEL" {
					return actool.Errorf("target must be WINWORD or EXCEL"), nil
				}
				secs := a.Seconds
				if secs <= 0 {
					secs = 600
				}
				if secs > 21600 {
					return actool.Errorf("seconds must be <= 21600 (6h)"), nil
				}
				mode := a.Mode
				if mode == "" {
					mode = "all"
				}
				seeds := a.Seeds
				if seeds == "" {
					seeds = `C:\fuzz\seeds`
				}
				out := a.Out
				if out == "" {
					out = `C:\fuzz\run-` + time.Now().UTC().Format("20060102T150405")
				}
				ps := fmt.Sprintf(`if (-not (Test-Path '%s')) { Write-Output 'FUZZER_MISSING (run action=deploy first)'; exit 3 }; $p = Start-Process powershell -WindowStyle Hidden -PassThru -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-File','%s','-Target','%s','-Seeds','%s','-Out','%s','-Seconds','%d','-Mode','%s'); Start-Sleep 5; if ($p.HasExited) { Write-Output ('START_FAILED exit=' + $p.ExitCode) } else { Write-Output ('CAMPAIGN_STARTED pid=' + $p.Id + ' out=' + '%s') }`,
					fuzzerPS, fuzzerPS, a.Target, seeds, out, secs, mode, out)
				ex, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
					"powershell -NoProfile -EncodedCommand "+psEncode64(ps), 30*time.Second)
				if err != nil {
					return actool.Errorf("start failed: " + err.Error()), nil
				}
				if strings.Contains(ex.Stdout, "START_FAILED") || strings.Contains(ex.Stdout, "FUZZER_MISSING") {
					return actool.Errorf("campaign failed to start: " + strings.TrimSpace(ex.Stdout)), nil
				}
				return jsonResult(map[string]any{"action": "start", "output": ex.Stdout, "target": a.Target, "seconds": secs, "mode": mode, "out": out})

			case "status", "crashes":
				dir := a.Out
				if dir == "" {
					// newest run dir
					ps := `$d = Get-ChildItem C:\fuzz -Directory -Filter 'run-*' | Sort-Object LastWriteTime -Descending | Select-Object -First 1; if ($d) { Write-Output $d.FullName } else { Write-Output 'NO_RUNS' }`
					ex, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
						"powershell -NoProfile -EncodedCommand "+psEncode64(ps), 20*time.Second)
					if err != nil {
						return actool.Errorf("status failed: " + err.Error()), nil
					}
					dir = strings.TrimSpace(strings.ReplaceAll(ex.Stdout, "\r\n", "\n"))
					if dir == "NO_RUNS" || dir == "" {
						return jsonResult(map[string]any{"action": a.Action, "output": "no campaign runs yet"})
					}
				}
				file := dir + `\state.json`
				if a.Action == "crashes" {
					file = dir + `\crashes\index.txt`
				}
				ps := fmt.Sprintf(`if (Test-Path '%s') { Get-Content '%s' -Raw } else { Write-Output 'NOT_FOUND' }`, file, file)
				ex, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
					"powershell -NoProfile -EncodedCommand "+psEncode64(ps), 20*time.Second)
				if err != nil {
					return actool.Errorf(a.Action + " failed: " + err.Error()), nil
				}
				return jsonResult(map[string]any{"action": a.Action, "run": dir, "output": ex.Stdout})

			case "stop":
				ps := `Get-CimInstance Win32_Process -Filter "Name like 'powershell%'" | Where-Object { $_.CommandLine -match 'artex-fuzzer' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force; Write-Output ('STOPPED ' + $_.ProcessId) }`
				ex, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
					"powershell -NoProfile -EncodedCommand "+psEncode64(ps), 20*time.Second)
				if err != nil {
					return actool.Errorf("stop failed: " + err.Error()), nil
				}
				return jsonResult(map[string]any{"action": "stop", "output": ex.Stdout})

			default:
				return actool.Errorf("action must be deploy|start|status|stop|crashes"), nil
			}
		}))
}

// b64 is a tiny helper (std base64 of a string).
func b64(s string) string {
	return base64Std([]byte(s))
}
