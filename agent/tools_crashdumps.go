package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
)

// crashDumpsTool: ONE place to find crash dumps on a Windows lab machine.
// Platform issue #6 — WER writes to its configured LocalDumps folder while
// some Office processes still land in %LOCALAPPDATA%\CrashDumps, and harnesses
// kept their own dump dirs; every intent re-derived its own two-dir watcher.
// This tool reads the WER config, scans every known location, and returns one
// newest-first list with full paths + age, so triage never misses a dump.
func (t *ToolSet) crashDumpsTool() actool.CoreTool {
	return readTool("crash_dumps",
		"Find crash dumps on a Windows machine in ONE call — scans every dump source (WER LocalDumps configured folder, %LOCALAPPDATA%\\CrashDumps, ProgramData\\Microsoft\\Windows\\WER, and any extra dirs passed) and returns one newest-first list: full path, process name parsed from the dump name, size, age. Use it after any trigger instead of writing your own two-dir watcher: pass since_seconds to only see dumps newer than the trigger, and the newest matching process's dump is your triage input for crash_triage_auto / map_decompile / windbg open_dump.",
		obj(map[string]any{
			"machine":       str("registered machine name"),
			"process":       str("optional: only dumps whose file name contains this process name (e.g. WINWORD, EXCEL)"),
			"since_seconds": map[string]any{"type": "integer", "description": "only dumps modified within the last N seconds (0 = all)"},
			"limit":         map[string]any{"type": "integer", "description": "max rows (default 20)"},
			"extra_dirs":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "additional directories to scan"},
			"username":      str("optional SSH username"),
		}, "machine"),
		t.needMachines("crash_dumps", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine      string   `json:"machine"`
				Process      string   `json:"process"`
				SinceSeconds int      `json:"since_seconds"`
				Limit        int      `json:"limit"`
				ExtraDirs    []string `json:"extra_dirs"`
				Username     string   `json:"username"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Limit <= 0 || a.Limit > 100 {
				a.Limit = 20
			}
			// One PowerShell round trip: read WER config, scan all dirs, emit
			// pipe-separated rows (no JSON depth issues over EncodedCommand).
			dirs := []string{"__WER__", "$env:LOCALAPPDATA\\CrashDumps", "C:\\ProgramData\\Microsoft\\Windows\\WER\\ReportArchive"}
			for _, d := range a.ExtraDirs {
				if safeVMPath(strings.TrimSpace(d)) {
					dirs = append(dirs, strings.ReplaceAll(strings.TrimSpace(d), "'", "''"))
				}
			}
			proc := strings.ToUpper(a.Process)
			ps := strings.Join([]string{
				`$dirs = @(` + strings.Join(quotePS(dirs), ",") + `) | Where-Object { $_ }`,
				`$wer = (Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows\Windows Error Reporting\LocalDumps' -ErrorAction SilentlyContinue).DumpFolder`,
				`$dirs = $dirs | ForEach-Object { if ($_ -eq '__WER__') { $wer } else { $_ } } | Where-Object { $_ }`,
				`$since = (Get-Date).AddSeconds(-` + fmt.Sprint(a.SinceSeconds) + `)`,
				`$rows = foreach ($d in $dirs) { Get-ChildItem $d -Filter *.dmp -Recurse -ErrorAction SilentlyContinue | ForEach-Object { $_ } }`,
				`$rows = $rows | Where-Object { $_.LastWriteTime -ge $since }`,
				fmt.Sprintf(`$rows = $rows | Where-Object { '%s' -eq '' -or $_.Name.ToUpper().Contains('%s') }`, proc, proc),
				`$rows | Sort-Object LastWriteTime -Descending | Select-Object -First ` + fmt.Sprint(a.Limit) + ` | ForEach-Object { '{0}|{1}|{2}' -f $_.FullName, $_.Length, $_.LastWriteTime.ToString('s') }`,
			}, "; ")
			ex, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, 0,
				"powershell -NoProfile -EncodedCommand "+psEncode64(ps), 40*time.Second)
			if err != nil {
				return actool.Errorf("dump scan failed: " + err.Error()), nil
			}
			type row struct {
				Path    string `json:"path"`
				Process string `json:"process"`
				Bytes   string `json:"bytes"`
				At      string `json:"at"`
			}
			var rows []row
			for _, l := range strings.Split(ex.Stdout, "\n") {
				parts := strings.Split(strings.TrimSpace(l), "|")
				if len(parts) < 3 {
					continue
				}
				rows = append(rows, row{Path: parts[0], Process: procFromDumpName(parts[0]), Bytes: parts[1], At: parts[2]})
			}
			if rows == nil {
				rows = []row{}
			}
			return jsonResult(map[string]any{
				"action": "crash_dumps", "machine": a.Machine,
				"count": len(rows), "dumps": rows,
				"note": "newest first; use the top matching dump as triage input (crash_triage_auto / open_dump)",
			})
		}))
}

// procFromDumpName extracts the process from a WER dump file name
// (EXCEL.EXE.1234.dmp -> EXCEL.EXE).
func procFromDumpName(path string) string {
	base := path
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.Index(base, ".dmp"); i > 0 {
		base = base[:i]
	}
	parts := strings.Split(base, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return base
}

func quotePS(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return out
}
