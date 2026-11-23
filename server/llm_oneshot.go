package server

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/TaxMachine/ARTEX/agent"
	"github.com/TaxMachine/ARTEX/third_party/norma/llm"
)

// oneShotLLM: a single non-tool LLM call used by the self-improvement loops
// (lesson extraction, triage critique, worker self-critique, rubric stats
// notes). Model: the configured fast profile — GLM 5.3 Flash with high
// reasoning effort by default (see dreamerProfile), falling back to disabled
// (empty string, no error) when no profile resolves so callers can skip.

func (s *Server) oneShotLLM(ctx context.Context, system, user string, maxTokens int) (string, error) {
	prof, err := s.dreamerProfile()
	if err != nil || prof == nil {
		return "", nil // disabled: caller skips the enhancement
	}
	cfg := agent.ConfigFrom(prof.Format, prof.Model, prof.BaseURL, prof.APIKey, prof.Proxy)
	cfg.ReasoningEffort = "high"
	prov, err := cfg.NewProvider()
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	msg, _, _, err := prov.Complete(cctx, llm.CompletionRequest{
		System: []string{system},
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(user)}},
		},
		MaxTokens: maxTokens,
	})
	if err != nil {
		return "", err
	}
	return extractText(msg), nil
}

// stripFence removes one surrounding markdown fence (LLMs love them).
func stripFenceOnce(s string) string { return stripFence(s) }

var _ = strings.TrimSpace

const triageCritiqueSystem = `You are a debugger's reviewer. Given a crash-triage extraction (exception, faulting module+offset, stack, claimed primitive), critique it in STRICT JSON:
{"sound": true|false, "issues": ["..."], "confidence": "low"|"medium"|"high"}
Check: does the claimed primitive follow from the exception code and access address (read vs write vs exec; near-null vs wild)? Is the offset plausible for the module? Do the stack frames support the story? Is anything overstated (crash labeled write-primitive when the code says read)? "issues" empty when sound. Max 3 issues, each one sentence.`

// wireTriageCritique installs the judge-the-judge pass on crash_triage_auto.
func (s *Server) wireTriageCritique() {
	if strings.ToLower(strings.TrimSpace(getEnv("ARTEX_RSI_CRITIQUE", "1"))) == "0" {
		return
	}
	agent.SetTriageCritiqueHook(func(ctx context.Context, text, structured string) (string, error) {
		user := text + "\n" + clipStr(structured, 1500)
		if strings.TrimSpace(user) == "\n" {
			return "", nil
		}
		return s.oneShotLLM(ctx, triageCritiqueSystem, clipStr(user, 4000), 500)
	})
}

func getEnv(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}
