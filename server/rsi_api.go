package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/TaxMachine/ARTEX/rsi"
)

// Dream-RSI operator endpoints. All read/evaluate; the only state change
// (adopt) is explicit and logged — the RSI loop's redeploy step goes through
// the operator, not autonomy.

func (s *Server) rsiExportTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TaskID int64  `json:"task_id"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TaskID <= 0 {
		writeErr(w, 400, "task_id is required")
		return
	}
	if body.Name == "" {
		body.Name = "task-" + strconv.FormatInt(body.TaskID, 10)
	}
	id, err := s.rsiStore().ExportTask(r.Context(), body.TaskID, body.Name)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"world_id": id, "task_id": body.TaskID, "name": body.Name})
}

func (s *Server) rsiListWorlds(w http.ResponseWriter, r *http.Request) {
	list, err := s.rsiStore().ListWorlds(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) rsiRanking(w http.ResponseWriter, r *http.Request) {
	list, err := s.rsiStore().Ranking(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) rsiDream(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Incumbent int     `json:"incumbent"`
		Lambda    float64 `json:"lambda"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Incumbent <= 0 {
		body.Incumbent = 1 // the deployed policy
	}
	if body.Lambda <= 0 {
		body.Lambda = 0.1
	}
	out, err := s.dreamWithLLM(r.Context(), body.Incumbent, body.Lambda)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out["note"] = "adopt via POST /api/rsi/adopt to redeploy (operator-gated)"
	writeJSON(w, 200, out)
}

func (s *Server) rsiAdopt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code     string  `json:"code"`
		Note     string  `json:"note"`
		AvgScore float64 `json:"avg_score"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
		writeErr(w, 400, "code is required")
		return
	}
	// next version number
	var next int
	if err := s.m.pg.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM rsi_policies`).Scan(&next); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	st := s.rsiStore()
	if err := st.Adopt(r.Context(), rsi.Candidate{Code: body.Code, Note: body.Note}, next, body.AvgScore); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// deploy: the active policy is stored as the latest version; the planner
	// reads it at next planning round (activePolicyCode).
	writeJSON(w, 200, map[string]any{"version": next, "code": body.Code, "deployed": true})
}

func (s *Server) rsiTrajectory(w http.ResponseWriter, r *http.Request) {
	v, _ := strconv.Atoi(r.URL.Query().Get("version"))
	if v <= 0 {
		v = 1
	}
	rows, err := s.rsiStore().TrajectoryReport(r.Context(), v)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rows)
}

// rsiStore lazily builds the store.
func (s *Server) rsiStore() *rsi.Store {
	if s.rsi == nil {
		s.rsi = rsi.NewStore(s.m.pg)
	}
	return s.rsi
}
