package server

import (
	"net/http"
)

// taskContextFill returns the latest observed input-token counts for a task —
// the live "how full is the context" signal, from llm_usage (the actual
// request sizes) split by role. The llm_context telemetry table exists
// (fork-sync) but is not yet written; llm_usage is authoritative today.
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
	writeJSON(w, 200, map[string]any{"task": taskID, "latest": out})
}
