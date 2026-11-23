package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/TaxMachine/ARTEX/db"
)

type taskLLMResolution struct {
	ProfileID *int64 `json:"profile_id,omitempty"`
	Name      string `json:"name"`
	Format    string `json:"format"`
	Model     string `json:"model"`
	Source    string `json:"source"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// ContextWindowK is the model's context window in K tokens and
	// ContextWindowSource says where it came from: "profile" (configured),
	// "remote" (published by the gateway's model listing) or "unknown". Zero
	// means unknown — clients show the raw request size instead of inventing a
	// denominator.
	ContextWindowK      int    `json:"context_window_k"`
	ContextWindowSource string `json:"context_window_source"`
}

func (s *Server) resolutionFromProfile(ctx context.Context, p *db.LLMProfile, source string) taskLLMResolution {
	if p == nil {
		return taskLLMResolution{Source: source, Reason: "LLM profile does not exist"}
	}
	id := p.ID
	result := taskLLMResolution{
		ProfileID: &id,
		Name:      p.Name,
		Format:    p.Format,
		Model:     p.Model,
		Source:    source,
	}
	result.ContextWindowK, result.ContextWindowSource = s.resolveContextWindowK(ctx, p)
	// Ollama profiles are keyless by design; the other formats need a key.
	if p.APIKey == "" && p.Format != "ollama" {
		result.Reason = "LLM profile has no API Key set"
		return result
	}
	if _, _, ok := s.providerForProfile(p.ID); !ok {
		result.Reason = "LLM profile format or parameters are invalid"
		return result
	}
	result.Available = true
	return result
}

// resolveTaskRoleLLM mirrors taskLLMRuntime.currentFor without creating a provider:
// role Agent binding -> explicit task chain -> active/global environment config.
// Database failures are returned rather than represented as an unavailable model.
func (s *Server) resolveTaskRoleLLM(ctx context.Context, t *Task, agentKey string) (taskLLMResolution, error) {
	if s == nil || s.m == nil || s.m.pg == nil {
		return taskLLMResolution{}, fmt.Errorf("database unavailable")
	}
	a, err := s.m.pg.GetAgentByKey(agentKey)
	if err != nil {
		return taskLLMResolution{}, fmt.Errorf("load agent %q: %w", agentKey, err)
	}
	if a != nil && a.LLMProfileID != nil {
		p, err := s.m.pg.ProfileByID(*a.LLMProfileID)
		if err != nil {
			return taskLLMResolution{}, fmt.Errorf("load agent LLM profile: %w", err)
		}
		if p != nil {
			resolved := s.resolutionFromProfile(ctx, p, "agent_binding")
			if resolved.Available {
				return resolved, nil
			}
		}
	}
	state := t.llmStateSnapshot()
	if len(state.ProfileIDs) > 0 {
		if state.ActiveID == nil {
			return taskLLMResolution{Source: "task_chain", Reason: "task LLM profile chain quota exhausted"}, nil
		}
		p, err := s.m.pg.ProfileByID(*state.ActiveID)
		if err != nil {
			return taskLLMResolution{}, fmt.Errorf("load task LLM profile: %w", err)
		}
		return s.resolutionFromProfile(ctx, p, "task_chain"), nil
	}
	p, err := s.m.pg.ActiveProfile()
	if err != nil {
		return taskLLMResolution{}, fmt.Errorf("load global LLM profile: %w", err)
	}
	if p != nil {
		resolved := s.resolutionFromProfile(ctx, p, "global_profile")
		if resolved.Available {
			return resolved, nil
		}
	}
	s.cfgMu.Lock()
	on, name, cfg := s.llmOn, s.llmProf, s.llmCfg
	s.cfgMu.Unlock()
	if on {
		if name == "" {
			name = "global configuration"
		}
		env := taskLLMResolution{
			Name: name, Format: cfg.Provider(), Model: cfg.Model,
			Source: "environment", Available: true,
			ContextWindowSource: "unknown",
		}
		// Only an explicitly configured window counts; the runtime's 200K
		// fallback is a compaction target, not a statement about the model.
		if cfg.ContextWindowK > 0 {
			env.ContextWindowK, env.ContextWindowSource = cfg.ContextWindowK, "profile"
		}
		return env, nil
	}
	return taskLLMResolution{Source: "global", Reason: "no available LLM profile"}, nil
}

func (s *Server) taskLLMResolutionHandler(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	roles := map[string]taskLLMResolution{}
	for responseKey, agentKey := range map[string]string{
		"mainagent": "mainagent",
		"planner":   "planner",
		"worker":    "worker",
	} {
		resolved, err := s.resolveTaskRoleLLM(r.Context(), t, agentKey)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		roles[responseKey] = resolved
	}
	writeJSON(w, http.StatusOK, roles)
}
