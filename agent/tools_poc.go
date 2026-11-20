package agent

import (
	"github.com/TaxMachine/ARTEX/db"

	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
)

// pocPackageTool: assemble a ready-to-run PoC zip for a finding. The agent
// writes the runbook; VM-side artifacts (crash inputs, payloads, scripts) are
// fetched over SFTP into files/ and a run.ps1 one-click runner is generated.
// The finding's page streams exactly this zip to the operator.
func (t *ToolSet) pocPackageTool() actool.CoreTool {
	return writeTool("poc_package",
		"Assemble a ready-to-run PoC zip for a finding — the MANDATORY last step after report_finding for every confirmed finding. Package = README.md runbook (environment, prerequisites, exact reproduction steps, expected outcome, cleanup) + files fetched from the machine over SFTP (crash input, minimized repro, payloads, scripts) + run.ps1 one-click runner generated from steps. The operator downloads exactly this zip from the finding's page and must be able to reproduce the finding from it alone: relative paths, no ARTEX infrastructure, everything needed offline.",
		obj(map[string]any{
			"finding_id": idp("standalone finding id (first line of report_finding output)"),
			"machine":    str("registered machine holding the files (required when files are listed)"),
			"files": map[string]any{"type": "array", "items": obj(map[string]any{
				"vm_path":     str("absolute path on the machine"),
				"pkg_path":    str("path inside the zip, e.g. files/min1_row0.xlsx (default files/<basename>)"),
				"description": str("what this file is (goes into the README's file list)"),
			}, "vm_path"), "description": "artifacts to fetch from the machine into the zip"},
			"readme":  str("full runbook markdown: environment, prerequisites, exact reproduction steps, expected outcome, cleanup"),
			"steps":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "one-click runner steps (PowerShell; becomes run.ps1 in the zip)"},
			"summary": str("one-line what this PoC demonstrates (shown as the package note on the finding)"),
		}, "finding_id", "readme"),
		t.needMachines("poc_package", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if defaultPoCStore == nil {
				return actool.Errorf("poc_package is not available on this server (no PoC store)"), nil
			}
			t.pocStore = defaultPoCStore
			var a struct {
				FindingID int64  `json:"finding_id"`
				Machine   string `json:"machine"`
				Readme    string `json:"readme"`
				Summary   string `json:"summary"`
				Steps     []string
				Files     []struct {
					VmPath  string `json:"vm_path"`
					PkgPath string `json:"pkg_path"`
					Desc    string `json:"description"`
				} `json:"files"`
			}
			_ = json.Unmarshal(in, &a)
			if a.FindingID <= 0 {
				return actool.Errorf("finding_id is required"), nil
			}
			if strings.TrimSpace(a.Readme) == "" {
				return actool.Errorf("readme (the runbook) is required — the operator must be able to run the PoC from it alone"), nil
			}
			if len(a.Files) > 0 && strings.TrimSpace(a.Machine) == "" {
				return actool.Errorf("machine is required when files are listed"), nil
			}
			f, err := knowledgeRepo.d.GetFinding(a.FindingID)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("finding %d: %v", a.FindingID, err)), nil
			}
			_ = t.setFindingPoC(a.FindingID, "pending", "", "package building")

			stage, err := os.MkdirTemp("", "artex-poc-*")
			if err != nil {
				_ = t.setFindingPoC(a.FindingID, "failed", "", err.Error())
				return actool.Errorf(err.Error()), nil
			}
			defer os.RemoveAll(stage)

			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			var fileRows []string
			var failed []string
			for _, fl := range a.Files {
				if strings.TrimSpace(fl.VmPath) == "" {
					continue
				}
				if err := func() error {
					base := filepath.Base(strings.ReplaceAll(fl.VmPath, "\\", "/"))
					local := filepath.Join(stage, base)
					if _, err := t.machines.Download(ctx, t.taskID, a.Machine, "", 0, fl.VmPath, local); err != nil {
						return err
					}
					data, err := os.ReadFile(local)
					if err != nil {
						return err
					}
					pkg := fl.PkgPath
					if pkg == "" {
						pkg = "files/" + base
					}
					pkg = strings.ReplaceAll(pkg, "\\", "/")
					if pathEscapes(pkg) {
						return fmt.Errorf("pkg_path escapes the zip")
					}
					w, err := zw.Create(pkg)
					if err != nil {
						return err
					}
					if _, err := w.Write(data); err != nil {
						return err
					}
					desc := fl.Desc
					if desc == "" {
						desc = "artifact from the machine"
					}
					fileRows = append(fileRows, fmt.Sprintf("- `%s` — %s", pkg, desc))
					return nil
				}(); err != nil {
					failed = append(failed, fl.VmPath+" ("+err.Error()+")")
				}
			}

			readme := a.Readme
			if !strings.HasPrefix(readme, "#") {
				readme = "# PoC — " + f.Name + "\n\n" + readme
			}
			readme += "\n\n## Package contents\n\n"
			if len(fileRows) > 0 {
				readme += strings.Join(fileRows, "\n") + "\n"
			} else {
				readme += "- README.md (this file)\n"
			}
			readme += "- run.ps1 (one-click runner)\n"
			readme += fmt.Sprintf("\nFinding: %s · class %s · severity %s\n", f.Name, f.VulnClass, f.Severity)
			if w, err := zw.Create("README.md"); err == nil {
				_, _ = w.Write([]byte(readme))
			}

			if len(a.Steps) > 0 {
				var b strings.Builder
				b.WriteString("# One-click PoC runner (generated by ARTEX poc_package)\n")
				b.WriteString("# Run from the directory containing 'files'.\n")
				b.WriteString("$ErrorActionPreference = 'Continue'\n")
				for _, st := range a.Steps {
					b.WriteString(st + "\n")
				}
				if w, err := zw.Create("run.ps1"); err == nil {
					_, _ = w.Write([]byte(b.String()))
				}
			}
			if err := zw.Close(); err != nil {
				_ = t.setFindingPoC(a.FindingID, "failed", "", "zip build failed: "+err.Error())
				return actool.Errorf("zip build failed: " + err.Error()), nil
			}
			p, err := t.pocStore.Save(a.FindingID, buf.Bytes())
			if err != nil {
				_ = t.setFindingPoC(a.FindingID, "failed", "", "save failed: "+err.Error())
				return actool.Errorf("save failed: " + err.Error()), nil
			}
			note := a.Summary
			if note == "" {
				note = fmt.Sprintf("%d file(s), %d runner step(s)", len(fileRows), len(a.Steps))
			}
			if len(failed) > 0 {
				note += fmt.Sprintf("; FAILED to fetch: %s", strings.Join(failed, ", "))
			}
			_ = t.setFindingPoC(a.FindingID, "ready", p, note)
			return jsonResult(map[string]any{
				"finding_id": a.FindingID, "poc_status": "ready", "path": p,
				"files_packaged": len(fileRows), "files_failed": failed,
				"note": "PoC package ready — the operator downloads it from the finding's page",
			})
		}))
}

// pathEscapes reports whether a zip-relative path climbs out via ...
func pathEscapes(p string) bool {
	for _, seg := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// setFindingPoC updates the finding's PoC build state through the repo.
func (t *ToolSet) setFindingPoC(id int64, status, path, note string) error {
	if knowledgeRepo.d == nil {
		return nil
	}
	return knowledgeRepo.d.SetFindingPoC(id, status, path, note)
}

var _ = io.Discard

// SetPoCStore installs the PoC zip store onto every ToolSet created after it.
func SetPoCStore(s *db.PoCStore) { defaultPoCStore = s }

var defaultPoCStore *db.PoCStore
