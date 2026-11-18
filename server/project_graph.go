package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// projectGraph returns the full canvas graph of a project: every binary asset
// (each version its own node), every finding, every task, and — when crossmap
// has indexed the project's binaries — every function with its call/import
// edges. One response the force-directed canvas renders directly.
func (s *Server) projectGraph(w http.ResponseWriter, r *http.Request) {
	p, err := s.m.pg.GetProjectByKey(r.PathValue("key"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if p == nil {
		writeErr(w, 404, "project not found")
		return
	}

	type gnode struct {
		ID    string         `json:"id"`
		Group string         `json:"group"` // binary | finding | task | function | import | entry
		Label string         `json:"label"`
		Sub   string         `json:"sub,omitempty"`
		Color string         `json:"color,omitempty"`
		Extra map[string]any `json:"extra,omitempty"`
	}
	type gedge struct {
		From string `json:"from"`
		To   string `json:"to"`
		Kind string `json:"kind"` // has_finding | produced_by | staged | call | import | dispatch | ioctl
	}
	nodes := []gnode{}
	edges := []gedge{}

	// Binaries: every staged generation of every binary asset in the project.
	binRows, err := s.m.pg.Query(`SELECT id, app_name, COALESCE(version,''), category, COALESCE(extra->>'path',''), COALESCE(extra->>'sha256',''), COALESCE(extra->>'os_build','')
FROM assets WHERE project_id=$1 AND type='binary' ORDER BY app_name, version`, p.ID)
	if err == nil {
		defer binRows.Close()
		for binRows.Next() {
			var id int64
			var name, version, kind, path, sha, osb string
			if binRows.Scan(&id, &name, &version, &kind, &path, &sha, &osb) != nil {
				continue
			}
			nid := fmt.Sprintf("binary:%d", id)
			label := name
			if version != "" {
				label += " " + version
			}
			nodes = append(nodes, gnode{ID: nid, Group: "binary", Label: label, Sub: kind,
				Extra: map[string]any{"asset_id": id, "path": path, "sha256": sha, "os_build": osb}})
			// Project -> binary edge
			edges = append(edges, gedge{From: "project:" + p.Key, To: nid, Kind: "staged"})
		}
	}

	// Findings.
	fRows, err := s.m.pg.Query(`SELECT id, COALESCE(name,''), severity, status, COALESCE(vulnclass,'')
FROM findings WHERE project_id=$1 ORDER BY id`, p.ID)
	if err == nil {
		defer fRows.Close()
		for fRows.Next() {
			var id int64
			var name, severity, status, vulnclass string
			if fRows.Scan(&id, &name, &severity, &status, &vulnclass) != nil {
				continue
			}
			nid := fmt.Sprintf("finding:%d", id)
			nodes = append(nodes, gnode{ID: nid, Group: "finding", Label: name, Sub: severity, Color: severity,
				Extra: map[string]any{"finding_id": id, "status": status, "vulnclass": vulnclass}})
			edges = append(edges, gedge{From: "project:" + p.Key, To: nid, Kind: "has_finding"})
		}
	}

	// Tasks.
	tRows, err := s.m.pg.Query(`SELECT id, COALESCE(name,''), status FROM tasks WHERE project_id=$1 AND deleted_at IS NULL ORDER BY id`, p.ID)
	if err == nil {
		defer tRows.Close()
		for tRows.Next() {
			var id int64
			var name, status string
			if tRows.Scan(&id, &name, &status) != nil {
				continue
			}
			nid := fmt.Sprintf("task:%d", id)
			nodes = append(nodes, gnode{ID: nid, Group: "task", Label: name, Sub: status,
				Extra: map[string]any{"task_id": id, "status": status}})
			edges = append(edges, gedge{From: "project:" + p.Key, To: nid, Kind: "produced_by"})
		}
	}

	// Project root node.
	nodes = append(nodes, gnode{ID: "project:" + p.Key, Group: "project", Label: p.Name, Sub: p.Key})

	// Function graph from crossmap for the project's binaries.
	crossmapNodes := 0
	crossmapEdges := 0
	if s.crossmapURL != "" && len(p.Binaries) > 0 {
		client := &http.Client{Timeout: 20 * time.Second}
		u := s.crossmapURL + "/map/graph?binaries=" + url.QueryEscape(strings.Join(p.Binaries, ","))
		resp, err := client.Get(u)
		if err == nil {
			var g struct {
				Nodes []struct {
					Binary string `json:"binary"`
					Name   string `json:"name"`
					Kind   string `json:"kind"`
				} `json:"nodes"`
				Edges []struct {
					From string `json:"from"`
					To   string `json:"to"`
					Kind string `json:"kind"`
				} `json:"edges"`
			}
			if json.NewDecoder(resp.Body).Decode(&g) == nil {
				for _, fn := range g.Nodes {
					// id: fn:<binary>!<name>
					fid := "fn:" + fn.Binary + "!" + fn.Name
					group := "function"
					switch fn.Kind {
					case "entry":
						group = "entry"
					case "import_thunk":
						group = "import"
					case "ioctl":
						group = "ioctl"
					}
					nodes = append(nodes, gnode{ID: fid, Group: group, Label: fn.Name, Sub: fn.Binary})
					// link the function's binary node to it
					crossmapNodes++
				}
				for _, fe := range g.Edges {
					edges = append(edges, gedge{From: "fn:" + fe.From, To: "fn:" + fe.To, Kind: fe.Kind})
					crossmapEdges++
				}
			}
		}
		resp.Body.Close()
	}

	writeJSON(w, 200, map[string]any{
		"project": p,
		"nodes":   nodes,
		"edges":   edges,
		"crossmap": map[string]any{
			"function_nodes": crossmapNodes,
			"function_edges": crossmapEdges,
		},
	})
}

var _ = db.Project{}
