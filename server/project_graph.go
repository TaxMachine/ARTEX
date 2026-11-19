package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/TaxMachine/ARTEX/db"
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
		}
	}

	// The canvas shows the software's assets only: no project ball, no task
	// nodes. Findings surface as highlighted functions inside the binaries.

	// Crossmap rollup: binary-level stats first (the function layer loads on
	// expand from the canvas). Binary nodes gain function/hub counts and an
	// indexed flag; entry counts feed the entry-surface glow.
	crossmapSummaries := map[string]map[string]int{}
	if s.crossmapURL != "" {
		client := &http.Client{Timeout: 20 * time.Second}
		var sum struct {
			Binaries map[string]struct {
				Functions int `json:"functions"`
				Entries   int `json:"entries"`
				Hubs      int `json:"hubs"`
			} `json:"binaries"`
		}
		if resp, err := client.Get(s.crossmapURL + "/map/summary"); err == nil {
			if json.NewDecoder(resp.Body).Decode(&sum) == nil {
				for b, st := range sum.Binaries {
					crossmapSummaries[b] = map[string]int{"functions": st.Functions, "entries": st.Entries, "hubs": st.Hubs}
				}
			}
			resp.Body.Close()
		}
	}
	lower := func(x string) string { return strings.ToLower(x) }
	// Version generation flags: latest version per app_name is the node the
	// initial view shows; older generations stay reachable via the toggle.
	latest := map[string]string{}
	for i := range nodes {
		if nodes[i].Group != "binary" {
			continue
		}
		name := nodes[i].Label
		if sp := strings.Fields(name); len(sp) > 0 {
			name = sp[0]
		}
		ver := ""
		if sp := strings.Fields(nodes[i].Label); len(sp) > 1 {
			ver = sp[1]
		}
		if prev, ok := latest[lower(name)]; !ok || ver > prev {
			latest[lower(name)] = ver
		}
	}

	// Decorate binary nodes with map stats + finding counts. crossmap binary
	// keys carry whatever case the .i64 filename had (CLFS.sys vs clfs.sys):
	// match case-insensitively.
	summaryByLower := map[string]map[string]any{}
	for b, st := range crossmapSummaries {
		// several .i64 names can differ only by case (clfs.sys vs CLFS.sys);
		// keep the richest one (the fully-analyzed database, not a PE-pass stub).
		best := map[string]any{"functions": st["functions"], "hubs": st["hubs"], "name": b}
		if prev, ok := summaryByLower[lower(b)]; !ok || st["functions"] > prev["functions"].(int) {
			summaryByLower[lower(b)] = best
		}
	}
	findingCountByLower := map[string]int{}
	for _, n := range nodes {
		if n.Group == "finding" {
			continue
		}
	}
	for i := range nodes {
		if nodes[i].Group != "binary" {
			continue
		}
		name := nodes[i].Label
		if sp := strings.Fields(name); len(sp) > 0 {
			name = sp[0]
		}
		if st, ok := summaryByLower[lower(name)]; ok {
			if nodes[i].Extra == nil {
				nodes[i].Extra = map[string]any{}
			}
			nodes[i].Extra["functions"] = st["functions"]
			nodes[i].Extra["hubs"] = st["hubs"]
			nodes[i].Extra["indexed"] = true
			nodes[i].Extra["name"] = st["name"]
		}
		nodes[i].Extra["findings"] = findingCountByLower[lower(name)]
		if sp := strings.Fields(nodes[i].Label); len(sp) > 1 {
			nodes[i].Extra["version"] = sp[1]
			nodes[i].Extra["is_latest"] = sp[1] == latest[lower(name)]
		}
	}

	totalFns := 0
	for _, st := range crossmapSummaries {
		totalFns += st["functions"]
	}
	writeJSON(w, 200, map[string]any{
		"project":  p,
		"nodes":    nodes,
		"edges":    edges,
		"crossmap": map[string]any{"function_total": totalFns, "binaries_indexed": len(crossmapSummaries)},
	})
}

var _ = db.Project{}
