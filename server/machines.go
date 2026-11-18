package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// Native machine HTTP API. Machines are operator-registered; the agent tools use
// the same rows but can only name a machine, never supply a host/domain/script.

func (s *Server) listMachines(w http.ResponseWriter, _ *http.Request) {
	list, err := s.m.Machines().List()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) createMachine(w http.ResponseWriter, r *http.Request) {
	var m db.Machine
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	m.ID = 0
	out, err := s.m.Machines().DB().Machines().Create(m)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, out)
}

func (s *Server) updateMachine(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid machine id")
		return
	}
	var m db.Machine
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	m.ID = id
	out, err := s.m.Machines().DB().Machines().Update(m)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) deleteMachine(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid machine id")
		return
	}
	if err := s.m.Machines().DB().Machines().Delete(id); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) machineStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.m.Machines().Status(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, st)
}

// machineLifecycle handles start|stop|reset with no task attribution (operator
// actions). Agent-initiated lifecycle goes through the machine_* tools and is
// recorded with the task id.
func (s *Server) machineLifecycle(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	name := r.PathValue("name")
	var force bool
	if r.Body != nil {
		var body struct {
			Force bool `json:"force"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		force = body.Force
	}
	var (
		st  any
		err error
	)
	switch action {
	case "start":
		st, err = s.m.Machines().Start(r.Context(), 0, name)
	case "stop":
		st, err = s.m.Machines().Stop(r.Context(), 0, name, force)
	case "reset":
		st, err = s.m.Machines().Reset(r.Context(), 0, name)
	default:
		writeErr(w, 400, "action must be start|stop|reset")
		return
	}
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) machineSnapshot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action   string `json:"action"`
		Snapshot string `json:"snapshot"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	out, err := s.m.Machines().Snapshot(r.Context(), 0, r.PathValue("name"), body.Action, body.Snapshot)
	if err != nil {
		writeErr(w, 409, err.Error()+"\n"+out)
		return
	}
	writeJSON(w, 200, map[string]any{"output": out})
}

func (s *Server) listMachineEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := db.MachineEventFilter{
		Machine: strings.TrimSpace(q.Get("machine")),
		Action:  strings.TrimSpace(q.Get("action")),
		Query:   strings.TrimSpace(q.Get("q")),
	}
	f.TaskID, _ = strconv.ParseInt(q.Get("task_id"), 10, 64)
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	list, err := s.m.Machines().DB().Machines().Events(f)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// The UI list does not need full command output; detail is one call away.
	for i := range list {
		list[i].Stdout = clip(list[i].Stdout, 600)
		list[i].Stderr = clip(list[i].Stderr, 300)
	}
	writeJSON(w, 200, list)
}

func (s *Server) getMachineEvent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid event id")
		return
	}
	taskID, _ := strconv.ParseInt(r.URL.Query().Get("task_id"), 10, 64)
	e, err := s.m.Machines().DB().Machines().Event(id, taskID)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, e)
}

func (s *Server) listMachineCaptures(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.m.Machines().Captures(r.PathValue("name"), limit)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// machineAutostart boots the machines linked to active tasks (or one task when
// task_id is supplied). This is the same sequence main runs before task resume.
func (s *Server) machineAutostart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TaskID int64 `json:"task_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	res, err := s.m.Machines().Autostart(r.Context(), body.TaskID, nil)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"results": res})
}
