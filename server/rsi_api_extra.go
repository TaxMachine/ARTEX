package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/TaxMachine/ARTEX/rsi"
)

// rsiWorldDetail: one world with its tree (nodes+edges) for graph rendering.
func (s *Server) rsiWorldDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid world id")
		return
	}
	world, err := s.rsiStore().LoadWorld(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "world not found")
		return
	}
	// render graph edges (parent links)
	type edge struct {
		Source int64 `json:"source"`
		Target int64 `json:"target"`
	}
	edges := []edge{}
	for _, n := range world.Nodes {
		if n.Parent != 0 && n.Parent != n.ID {
			edges = append(edges, edge{Source: n.Parent, Target: n.ID})
		}
	}
	writeJSON(w, 200, map[string]any{
		"id": world.ID, "task_id": world.TaskID, "name": world.Name,
		"root": world.Root, "B": world.B, "S": world.S,
		"nodes": world.Nodes, "links": edges,
	})
}

// rsiPolicyList: every policy version with its average replay score.
func (s *Server) rsiPolicyList(w http.ResponseWriter, r *http.Request) {
	list, err := s.rsiStore().Ranking(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// current = highest version
	cur := 0
	for _, p := range list {
		if v, _ := p["version"].(int); v > cur {
			cur = v
		}
	}
	writeJSON(w, 200, map[string]any{"policies": list, "deployed_version": cur})
}

// rsiActivePolicy: the currently deployed policy's code.
func (s *Server) rsiActivePolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"code": s.activePolicyCode(r.Context())})
}

// rsiTriggerExport: POST /api/rsi/export {task_id} (existing handler kept);
// POST /api/rsi/re-dream runs one dreaming cycle without adoption (dry run).
func (s *Server) rsiRedream(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Incumbent int     `json:"incumbent"`
		Lambda    float64 `json:"lambda"`
		WorldIDs  []int64 `json:"world_ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Incumbent <= 0 {
		body.Incumbent = s.latestPolicyVersion(r.Context())
	}
	if body.Incumbent <= 0 {
		body.Incumbent = 1
	}
	if body.Lambda <= 0 {
		body.Lambda = 0.1
	}
	out, err := s.dreamWithLLMWorlds(r.Context(), body.Incumbent, body.Lambda, body.WorldIDs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out["note"] = "dry run — adopt via POST /api/rsi/adopt to deploy"
	writeJSON(w, 200, out)
}

func (s *Server) latestPolicyVersion(ctx context.Context) int {
	rows, err := s.m.pg.QueryContext(ctx, `SELECT COALESCE(MAX(version),0) FROM rsi_policies`)
	if err != nil {
		return 0
	}
	defer rows.Close()
	v := 0
	if rows.Next() {
		_ = rows.Scan(&v)
	}
	return v
}

var _ = rsi.SeedPolicy{}
