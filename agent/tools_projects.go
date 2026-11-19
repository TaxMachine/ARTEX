package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
)

// Project context tools: tasks on the same target software accumulate under a
// project, and every task in it sees what the others found — prior findings,
// binaries (patch generations), and the crossmap function graph.

func (t *ToolSet) listProjects() actool.CoreTool {
	return readTool("list_projects",
		"List the software projects (one per target software). Each row: key, name, task/finding/binary counts. A task linked to a project inherits its context: check this first when starting work on software that may have been targeted before.",
		obj(map[string]any{}),
		t.needMachines("list_projects", func(ctx context.Context, _ json.RawMessage) (actool.Result, error) {
			// projects live in the platform store, reached through the machines
			// service's DB handle (same process-wide pool).
			list, err := t.machines.DB().ListProjects()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{"projects": list})
		}))
}

func (t *ToolSet) projectContext() actool.CoreTool {
	return readTool("project_context",
		"Read the accumulated context of this task's software project: prior findings (with severity/status/evidence versions), staged binaries (each version is its own row — the patch-diff pairs), prior task summaries, and which of the project's binaries are indexed in the crossmap function graph. Call it FIRST when the task is linked to a project, so you start from everything already known instead of re-deriving it.",
		obj(map[string]any{
			"key": str("optional project key; defaults to this task's project"),
		}),
		t.needMachines("project_context", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Key string `json:"key"`
			}
			_ = json.Unmarshal(in, &a)
			dbp := t.machines.DB()
			key := strings.TrimSpace(a.Key)
			if key == "" {
				if t.taskID <= 0 {
					return actool.Errorf("project_context needs a task context or an explicit key"), nil
				}
				p, err := dbp.ProjectForTask(t.taskID)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if p == nil {
					return actool.Errorf("this task has no project; pass key= explicitly or link one via set_task_project"), nil
				}
				key = p.Key
			}
			p, err := dbp.GetProjectByKey(key)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if p == nil {
				return actool.Errorf(fmt.Sprintf("no project %q (create it with save_project)", key)), nil
			}

			// Findings rollup.
			findings := []map[string]any{}
			rows, err := dbp.Query(`SELECT id, vulnclass, COALESCE(name,''), severity, status, verification_status
FROM findings WHERE project_id=$1 ORDER BY severity DESC, id DESC LIMIT 200`, p.ID)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var id int64
					var vulnclass, name, severity, status, verification string
					if rows.Scan(&id, &vulnclass, &name, &severity, &status, &verification) == nil {
						findings = append(findings, map[string]any{
							"id": id, "vulnclass": vulnclass, "name": name, "severity": severity,
							"status": status, "verification": verification,
						})
					}
				}
			}

			// Binary generations.
			binaries := []map[string]any{}
			brows, err := dbp.Query(`SELECT app_name, COALESCE(version,''), category, COALESCE(extra->>'path',''), COALESCE(extra->>'sha256','')
FROM assets WHERE project_id=$1 AND type='binary' ORDER BY app_name, version`, p.ID)
			if err == nil {
				defer brows.Close()
				for brows.Next() {
					var name, version, kind, path, sha string
					if brows.Scan(&name, &version, &kind, &path, &sha) == nil {
						binaries = append(binaries, map[string]any{
							"name": name, "version": version, "kind": kind, "path": path, "sha256": sha,
						})
					}
				}
			}

			// Prior tasks (id, name, status, one-line result).
			tasks := []map[string]any{}
			trows, err := dbp.Query(`SELECT id, COALESCE(name,''), status FROM tasks WHERE project_id=$1 AND deleted_at IS NULL ORDER BY id`, p.ID)
			if err == nil {
				defer trows.Close()
				for trows.Next() {
					var id int64
					var name, status string
					if trows.Scan(&id, &name, &status) == nil {
						tasks = append(tasks, map[string]any{"id": id, "name": name, "status": status})
					}
				}
			}

			return jsonResult(map[string]any{
				"project":  p,
				"findings": findings,
				"binaries": binaries,
				"tasks":    tasks,
				"note":     "everything here was produced by prior tasks on the same software; build on it instead of re-deriving",
			})
		}))
}

func (t *ToolSet) saveProject() actool.CoreTool {
	return writeTool("save_project",
		"Create (or update) a software project — the unit tasks on the same target accumulate under. NAMING RULE: name is THE PRODUCT NAME AND VERSION, never a binary or machine name — \"Microsoft 365 Apps (16.0.20625, Current channel)\", \"Windows 11 26H2 (build 26300.9974)\", \"WordPress 7.1.4\"; the component goes in the description (\"Word document parsing (WINWORD.EXE, WWLIB.DLL)\"), not the title. key is a slug (microsoft-365-16-0-20625, windows-11-26h2-26300, wordpress-7-1-4); binaries lists the crossmap binary names (file names: clfs.sys, WINWORD.EXE) so the function graph joins back. Returns the project.",
		obj(map[string]any{
			"name":        str("PRODUCT NAME AND VERSION, e.g. \"Microsoft 365 Apps (16.0.20625, Current channel)\" — never a lone binary/machine name"),
			"key":         str("slug; auto-derived from name when omitted"),
			"kind":        map[string]any{"type": "string", "enum": []string{"software", "product", "suite"}, "description": "default software"},
			"description": str("what this target is; components/binaries/machines go here, not in the name"),
			"binaries":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "binary names (file names) that belong to this project"},
		}, "name"),
		t.needMachines("save_project", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Name        string   `json:"name"`
				Key         string   `json:"key"`
				Kind        string   `json:"kind"`
				Description string   `json:"description"`
				Binaries    []string `json:"binaries"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Name) == "" && strings.TrimSpace(a.Key) == "" {
				return actool.Errorf("name or key is required"), nil
			}
			if strings.TrimSpace(a.Key) == "" {
				a.Key = a.Name
			}
			p, err := t.machines.DB().UpsertProject(a.Name, a.Key, a.Kind, a.Description, a.Binaries)
			if err != nil {
				return actool.Errorf("save_project failed: " + err.Error()), nil
			}
			return jsonResult(p)
		}))
}

func (t *ToolSet) setTaskProject() actool.CoreTool {
	return writeTool("set_task_project",
		"Link THIS task to a software project (created with save_project when missing). Once linked, project_context returns everything prior tasks on the same software found, and this task's findings/binaries roll up under the project too. Operators can also link from the task page. Pass the project key created by save_project (its name is the PRODUCT NAME AND VERSION, e.g. \"Microsoft 365 Apps (16.0.20625)\"); when no project exists yet, call save_project FIRST with a proper product name instead of letting this create a placeholder named after a binary or machine.",
		obj(map[string]any{
			"key": str("project key (or name; slugged automatically)"),
		}, "key"),
		t.needMachines("set_task_project", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Key string `json:"key"`
			}
			_ = json.Unmarshal(in, &a)
			if t.taskID <= 0 {
				return actool.Errorf("set_task_project is only available inside a task"), nil
			}
			if strings.TrimSpace(a.Key) == "" {
				return actool.Errorf("key is required"), nil
			}
			p, err := t.machines.DB().UpsertProject(a.Key, a.Key, "software", "", nil)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if err := t.machines.DB().SetTaskProject(t.taskID, p.ID); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{"linked": true, "project": p.Key, "project_id": p.ID})
		}))
}

var _ = fmt.Sprintf
