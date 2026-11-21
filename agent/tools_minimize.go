package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
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
			// Deploy the minimizer over SFTP (temp file -> Upload). The
			// original base64-inside-EncodedCommand transport silently
			// truncated on multi-KB bodies — a 0-byte script on disk made
			// every run a no-op. SFTP is byte-exact.
			check, cerr := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
				"powershell -NoProfile -EncodedCommand "+psEncode64(`if ((Get-Item 'C:\fuzz\artex-minimize.ps1' -ErrorAction SilentlyContinue).Length -eq `+fmt.Sprint(len(minimizeScript))+`) { Write-Output PRESENT } else { Write-Output MISSING }`), 20*time.Second)
			if cerr != nil || !strings.Contains(check.Stdout, "PRESENT") {
				tmp, terr := os.CreateTemp("", "artex-minimize-*.ps1")
				if terr != nil {
					return actool.Errorf("minimizer staging failed: " + terr.Error()), nil
				}
				tmpName := tmp.Name()
				defer os.Remove(tmpName)
				if _, terr = tmp.WriteString(minimizeScript); terr != nil {
					tmp.Close()
					return actool.Errorf("minimizer staging failed: " + terr.Error()), nil
				}
				tmp.Close()
				if _, uerr := t.machines.Upload(ctx, t.taskID, a.Machine, a.Username, 0, tmpName, `C:\fuzz\artex-minimize.ps1`); uerr != nil {
					return actool.Errorf("minimizer deploy failed: " + uerr.Error()), nil
				}
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
