package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/TaxMachine/ARTEX/db"
)

const maxWorkerMessageBytes = 64 << 10

func validWorkerMessageRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

// sendWorkerMessage continues a paused Worker intent with a human-authored message.
// The message is injected as the next turn's input through the same
// resume-from-transcript path the worker uses on a normal resume (ExecuteWithMessage),
// so this reuses the existing pause/resume machinery rather than a bespoke protocol.
// The run happens in a dedicated goroutine outside the worker pool (runDetachedIntent),
// so the message is picked up immediately even when every pool slot is busy — the same
// way the main-agent chat handler starts its run directly. In-memory only: a process
// restart re-runs the intent from its transcript without the message, which is
// acceptable for this rare interrupt-then-continue action.
func (s *Server) sendWorkerMessage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil || iid <= 0 {
		writeErr(w, http.StatusBadRequest, "bad intent id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkerMessageBytes)
	var req struct {
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "Request body too large")
			return
		}
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	message := strings.TrimSpace(req.Message)
	requestID := strings.TrimSpace(req.RequestID)
	if message == "" {
		writeErr(w, http.StatusBadRequest, "Message cannot be empty")
		return
	}
	if len([]rune(message)) > 4000 {
		writeErr(w, http.StatusBadRequest, "Message must not exceed 4000 characters")
		return
	}
	if !validWorkerMessageRequestID(requestID) {
		writeErr(w, http.StatusBadRequest, "request_id must be 1-128 characters of letters, digits, -, _, . or :")
		return
	}

	// Reject non-runnable task lifecycles up front so the caller gets a clear reason
	// instead of a silent no-op. The intent itself must be paused: the UI flow is
	// interrupt (pause) first, then send.
	if s.engine.IsDeleting(t.ID) {
		writeErr(w, http.StatusConflict, "Task is being deleted; cannot send a message to the Worker")
		return
	}
	lifecycle := t.lifecycleSnapshot()
	switch {
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		writeErr(w, http.StatusConflict, "Task is paused; resume the task before sending a message to the Worker")
		return
	case lifecycle.Queued:
		writeErr(w, http.StatusConflict, "A queued task cannot receive Worker messages")
		return
	case isTerminalStatus(lifecycle.Status):
		writeErr(w, http.StatusConflict, "A terminal task cannot receive Worker messages")
		return
	case s.engine.isSettling(t.ID):
		writeErr(w, http.StatusConflict, "Task is settling; cannot send a message to the Worker")
		return
	}

	node, err := t.Store.GetNode(iid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node == nil {
		if inherited, sourceErr := t.Store.GetNodeWithSources(iid); sourceErr == nil && inherited != nil && inherited.Inherited {
			writeErr(w, http.StatusConflict, "Inherited intents are read-only and cannot receive Worker messages")
			return
		}
		writeErr(w, http.StatusNotFound, "intent not found")
		return
	}
	if node.Kind != db.KindIntent {
		writeErr(w, http.StatusConflict, "node is not an intent")
		return
	}
	if node.State == "running" {
		// Opencode-style steering for a live work: queue the message into the
		// engine's steer box; the worker reads it right before its NEXT tool call
		// (the call is blocked and the message handed back, so it re-plans). The
		// human turn is recorded on the intent so it renders in the session.
		if err := s.engine.SteerWork(iid, message); err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		uid := iid
		s.engine.emitActivity(t, db.Activity{NodeID: &uid, Worker: "user", Kind: "user", Summary: message, Detail: message})
		writeJSON(w, http.StatusOK, map[string]any{
			"id":         iid,
			"state":      "running",
			"accepted":   true,
			"queued":     true,
			"request_id": requestID,
		})
		return
	}
	if node.State != "paused" {
		writeErr(w, http.StatusConflict, "Only a paused or running Worker accepts messages")
		return
	}
	agentMessage, ok := s.prepareChatMentionMessage(w, message)
	if !ok {
		return
	}

	// runDetachedIntent transitions paused->running, emits the user turn and starts a
	// dedicated run. Root the run at s.ctx so a disconnected browser cannot strand it
	// while task pause/delete/shutdown still stop it.
	if err := s.engine.runDetachedIntent(s.ctx, t, iid, requestID, message, agentMessage); err != nil {
		switch {
		case errors.Is(err, db.ErrIntentStateConflict):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":         iid,
		"state":      "running",
		"accepted":   true,
		"request_id": requestID,
	})
}

// sendPlannerMessage steers the task's planner opencode-style: the message is
// stored as an operator hint (origin human) the planner reads at its NEXT round
// (the hint trigger wakes it immediately when the task is running), and the
// human turn is recorded on the task's planner lane so it renders in the
// session transcript. Unlike worker steering there is no pre-tool-call
// injection point — planner rounds are discrete LLM calls — so the hint block
// + trigger is the delivery mechanism.
func (s *Server) sendPlannerMessage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkerMessageBytes)
	var req struct {
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "Request body too large")
			return
		}
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		writeErr(w, http.StatusBadRequest, "Message cannot be empty")
		return
	}
	if len([]rune(message)) > 4000 {
		writeErr(w, http.StatusBadRequest, "Message must not exceed 4000 characters")
		return
	}
	agentMessage, ok := s.prepareChatMentionMessage(w, message)
	if !ok {
		return
	}
	// Store as an operator hint: the planner's next round renders hints in its
	// situation block, and the trigger wakes it now (a paused/terminal task
	// still stores the hint; it is read when planning resumes).
	if _, err := t.Store.AddNode(db.KindHint, map[string]any{"text": agentMessage}, 0, "active", "human", nil); err != nil {
		writeErr(w, http.StatusInternalServerError, "hint store failed: "+err.Error())
		return
	}
	t.NotifyHint("human", []string{agentMessage})
	s.engine.emitActivity(t, db.Activity{Worker: "planner", Kind: "user", Summary: message, Detail: message})
	writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "queued": true, "request_id": req.RequestID})
}
