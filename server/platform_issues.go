package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"
)

// Platform-issue API: the operator's fix-later backlog, fed by the
// self-improvement loops and manually fileable.

func (s *Server) listPlatformIssues(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	list, err := s.m.PG().ListPlatformIssues(r.Context(), status, 200)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) createPlatformIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title     string `json:"title"`
		Detail    string `json:"detail"`
		Source    string `json:"source"`
		SourceRef string `json:"source_ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
		writeErr(w, 400, "title is required")
		return
	}
	if body.Source == "" {
		body.Source = "manual"
	}
	id, created, err := s.m.PG().CreatePlatformIssue(r.Context(), body.Title, body.Detail, body.Source, body.SourceRef)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "created": created})
}

func (s *Server) updatePlatformIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid issue id")
		return
	}
	var body struct {
		Status string `json:"status"` // resolved | wontfix | open
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		(body.Status != "resolved" && body.Status != "wontfix" && body.Status != "open") {
		writeErr(w, 400, "status must be resolved|wontfix|open")
		return
	}
	if err := s.m.PG().UpdatePlatformIssueStatus(r.Context(), id, body.Status, body.Note); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// filePlatformIssue is the internal path the self-improvement loops use.
// Best-effort: never fails the caller.
func (s *Server) filePlatformIssue(source, sourceRef, title, detail string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, _, err := s.m.PG().CreatePlatformIssue(ctx, title, detail, source, sourceRef); err != nil {
		log.Printf("[platform-issue] file failed (%s): %v", source, err)
	}
}
