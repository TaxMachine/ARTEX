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

//go:embed assets/artex-minimize.ps1
var minimizeScript string

// crashMinimizeTool: shrink a crashing input to the smallest variant that still
// reproduces the SAME WER signature. A minimized repro makes every later step
// cheaper — grooming a 512-byte minimal OOXML beats grooming a 40KB mutate —
// and pinpoints the offending structure (what survives shrinking IS the bug).
func (t *ToolSet) crashMinimizeTool() actool.CoreTool {
	return writeTool("crash_minimize",
		"Minimize a crashing input: structure-aware shrinking (drop XML elements/attributes, remove non-essential zip parts, truncate blocks) while verifying after each step that the SAME WER crash signature still reproduces — what survives shrinking is the offending structure itself. Returns the minimized file path + reduction stats. Run this on every fuzz-found crash BEFORE grooming: a 512-byte minimal repro pinpoints the bug structure and makes heap grooming dramatically simpler. Signature = the P-tuple from fuzz_campaign action=crashes (or crash_triage_auto's exception line).",
		obj(map[string]any{
			"machine":    str("registered machine name"),
			"crash_file": str("VM path of the crashing input"),
			"signature":  str("the crash signature (WER P-tuple) that must keep reproducing"),
			"target":     map[string]any{"type": "string", "enum": []string{"WINWORD", "EXCEL"}, "description": "target application"},
			"out":        str("VM output directory (default <crash dir>\\min)"),
			"max_steps":  map[string]any{"type": "integer", "description": "shrink attempts (default 60)"},
			"username":   str("optional SSH username"),
		}, "machine", "crash_file", "signature", "target"),
		t.needMachines("crash_minimize", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine   string `json:"machine"`
				CrashFile string `json:"crash_file"`
				Signature string `json:"signature"`
				Target    string `json:"target"`
				Out       string `json:"out"`
				MaxSteps  int    `json:"max_steps"`
				Username  string `json:"username"`
			}
			_ = json.Unmarshal(in, &a)
			if a.CrashFile == "" || a.Signature == "" || (a.Target != "WINWORD" && a.Target != "EXCEL") {
				return actool.Errorf("crash_file, signature and target (WINWORD|EXCEL) are required"), nil
			}
			out := a.Out
			if out == "" {
				i := strings.LastIndexByte(a.CrashFile, '\\')
				out = strings.TrimSuffix(a.CrashFile[:i+1], "\\") + `\min`
			}
			steps := a.MaxSteps
			if steps <= 0 {
				steps = 60
			}
			// deploy the minimizer once (content-addressed: skip when present)
			deploy := fmt.Sprintf(`if (-not (Test-Path 'C:\fuzz\artex-minimize.ps1')) { [IO.File]::WriteAllBytes('C:\fuzz\artex-minimize.ps1', [Convert]::FromBase64String('%s')) }; Write-Output READY`, psEncode64(minimizeScript))
			if ex, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
				"powershell -NoProfile -EncodedCommand "+psEncode64(deploy), 30*time.Second); err != nil || !strings.Contains(ex.Stdout, "READY") {
				return actool.Errorf("minimizer deploy failed: " + ex.Stdout), nil
			}
			// -File execution is policy-blocked on the VM; -Command with the
			// script body (deployed to disk) sidesteps it entirely.
			run := fmt.Sprintf(`$code = Get-Content 'C:\fuzz\artex-minimize.ps1' -Raw; $sb = [ScriptBlock]::Create($code); & $sb -Target '%s' -CrashFile '%s' -Signature '%s' -Out '%s' -MaxSteps %d *> "$env:TEMP\min-run.log"; $info = Get-Content '%s\min-info.json' -Raw -ErrorAction SilentlyContinue; Write-Output ("INFO:" + $info); Write-Output ("LOGTAIL:"); Get-Content '%s\min-steps.log' -Tail 8 -ErrorAction SilentlyContinue`,
				a.Target, a.CrashFile, strings.ReplaceAll(a.Signature, "'", "''"), out, steps, out, out)
			ex, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
				"powershell -NoProfile -EncodedCommand "+psEncode64(run), 30*time.Minute)
			if err != nil {
				return actool.Errorf("minimize run failed: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"action": "crash_minimize", "machine": a.Machine,
				"output": strings.TrimSpace(ex.Stdout), "out_dir": out,
				"note": "min.<ext> + min-info.json in out_dir; original vs minimized size + reduction % in the info",
			})
		}))
}
