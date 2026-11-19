package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Projects: one row per target software. Tasks link to a project (manually or
// auto-detected from the goal text); everything the project's tasks produce —
// findings, binaries, PoCs — rolls up per project, and a new task on the same
// software starts from that accumulated context.

func (s *Server) listProjects(w http.ResponseWriter, _ *http.Request) {
	list, err := s.m.pg.ListProjects()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) upsertProject(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Name        string   `json:"name"`
		Key         string   `json:"key"`
		Kind        string   `json:"kind"`
		Description string   `json:"description"`
		Binaries    []string `json:"binaries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	out, err := s.m.pg.UpsertProject(p.Name, p.Key, p.Kind, p.Description, p.Binaries)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, out)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.m.pg.GetProjectByKey(r.PathValue("key"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if p == nil {
		writeErr(w, 404, "project not found")
		return
	}
	// Rollups: tasks, findings, and the crossmap stats for the project's binaries.
	// Slices are initialized non-nil so an empty project serializes as [], not
	// null — the detail page reads .length on them.
	taskRows := []map[string]any{}
	rows, err := s.m.pg.Query(`SELECT id, name, status FROM tasks WHERE project_id=$1 AND deleted_at IS NULL ORDER BY id DESC LIMIT 50`, p.ID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id int64
			var name, status string
			if rows.Scan(&id, &name, &status) == nil {
				taskRows = append(taskRows, map[string]any{"id": id, "name": name, "status": status})
			}
		}
	}
	var findings []map[string]any = []map[string]any{}
	frows, err := s.m.pg.Query(`SELECT id, vulnclass, name, severity, status FROM findings WHERE project_id=$1 ORDER BY id DESC LIMIT 100`, p.ID)
	if err == nil {
		defer frows.Close()
		for frows.Next() {
			var id int64
			var vulnclass, name, severity, status string
			if frows.Scan(&id, &vulnclass, &name, &severity, &status) == nil {
				findings = append(findings, map[string]any{"id": id, "vulnclass": vulnclass, "name": name, "severity": severity, "status": status})
			}
		}
	}
	// Binary assets of the project: every staged generation with version, kind,
	// path, sha256 and OS build — the patch-diff pairs live here.
	assets := []map[string]any{}
	arows, err := s.m.pg.Query(`SELECT id, app_name, COALESCE(version,''), category,
	       COALESCE(extra->>'path',''), COALESCE(extra->>'sha256',''), COALESCE(extra->>'os_build',''), COALESCE(extra->>'arch','')
	FROM assets WHERE project_id=$1 AND type='binary' ORDER BY app_name, version`, p.ID)
	if err == nil {
		defer arows.Close()
		for arows.Next() {
			var id int64
			var name, version, kind, path, sha, osb, arch string
			if arows.Scan(&id, &name, &version, &kind, &path, &sha, &osb, &arch) == nil {
				assets = append(assets, map[string]any{
					"id": id, "name": name, "version": version, "kind": kind,
					"path": path, "sha256": sha, "os_build": osb, "arch": arch,
				})
			}
		}
	}
	crossmap := s.projectCrossmapStats(p.Binaries)
	writeJSON(w, 200, map[string]any{
		"project": p, "tasks": taskRows, "findings": findings, "assets": assets, "crossmap": crossmap,
	})
}

func (s *Server) setTaskProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	var body struct {
		Key string `json:"key"` // project key; empty = unlink
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var projectID int64
	if k := strings.TrimSpace(body.Key); k != "" {
		p, err := s.m.pg.UpsertProject(k, k, "software", "", nil)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		projectID = p.ID
	}
	if err := s.m.pg.SetTaskProject(id, projectID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "project_id": projectID})
}

// projectCrossmapStats reports whether each project binary is indexed in the
// crossmap function graph. Crossmap is optional: unreachable → available=false
// and the project view still renders.
func (s *Server) projectCrossmapStats(binaries []string) map[string]any {
	out := map[string]any{"available": false, "binaries": map[string]any{}}
	if s.crossmapURL == "" || len(binaries) == 0 {
		return out
	}
	client := &http.Client{Timeout: 5 * time.Second}
	bins := out["binaries"].(map[string]any)
	for _, b := range binaries {
		resp, err := client.Get(s.crossmapURL + "/map/search?binary=" + url.QueryEscape(b) + "&limit=1")
		if err != nil {
			return out
		}
		var res struct {
			Nodes []json.RawMessage `json:"nodes"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		out["available"] = true
		bins[b] = map[string]any{"indexed": len(res.Nodes) > 0}
	}
	return out
}
