package server

import (
	"net/http"

	"github.com/TaxMachine/ARTEX/db"
)

// taskContextFill returns the live context-fill signal for a task or a chat
// conversation (?task=<id> or conv-<id>): the input size of the most recent
// request, per role (latest) and — for tasks — per UI session (sessions:
// main:<seg> | plan | intent:<id>). Sourced from llm_usage, where the recorder
// writes one row per physical request with the raw session string; the activity
// table's kind='usage' rows are cumulative run totals and cannot serve.
//
// The context WINDOW is deliberately not part of this response: it is a
// property of the resolved model (see /api/tasks/{id}/llm/resolution), and
// when neither the profile nor the gateway states it the client shows the raw
// request size instead of a percentage against an invented denominator.
func (s *Server) taskContextFill(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task")
	if taskID == "" {
		writeErr(w, 400, "task is required")
		return
	}
	type row struct {
		Worker      string `json:"worker"`
		InputTokens int64  `json:"input_tokens"`
		CacheRead   int64  `json:"cache_read"`
		Ts          string `json:"ts"`
	}
	rows, err := s.m.pg.Query(`
		SELECT DISTINCT ON (worker) worker, input_tokens, cache_read, ts
		FROM llm_usage
		WHERE task_id = $1 AND input_tokens > 0
		ORDER BY worker, id DESC
		LIMIT 6`, taskID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []row{}
	for rows.Next() {
		var x row
		if rows.Scan(&x.Worker, &x.InputTokens, &x.CacheRead, &x.Ts) == nil {
			out = append(out, x)
		}
	}
	sessions := []db.SessionFill{}
	if t := s.m.ResolveTask(taskID); t != nil {
		fills, err := t.Store.SessionContextFill()
		if err == nil && fills != nil {
			sessions = fills
		}
	}
	writeJSON(w, 200, map[string]any{"task": taskID, "latest": out, "sessions": sessions})
}
