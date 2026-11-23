package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/TaxMachine/ARTEX/db"
	"github.com/TaxMachine/ARTEX/third_party/norma/harness"
)

// Post-run worker self-critique (Meta Self-Improving Post-Thinking, adapted):
// when an intent ends exhausted (budget/turns/time — where mistakes
// concentrate), one fast-model pass reviews the run's final summary and tool
// trail and writes a critique fact anchored to the intent. The planner reads
// it like any fact, so concrete catches (a leaked instrument, an unverified
// assumption stated as fact, a missed cheaper path) feed the next intent
// instead of vanishing with the session.

const selfCritiqueSystem = `You critique ONE completed worker run of an authorized pentest platform immediately after it ended (budget/turns exhausted).

Given the run's final summary and its tool-call trail (names + brief args), output STRICT JSON:
{"worth_facting": true|false, "critique": "2-4 sentences", "concrete_catches": ["..."], "platform_issues": [{"title":"...","detail":"..."}]}
Look for: stated-as-fact claims without recorded evidence; instrument or environment state likely left changed (page_heap, debugger sessions, spawned processes, captures) that taints later runs; wasted budget (repeated near-identical calls, dead-end retried); write-backs that may not have landed before exhaustion.
platform_issues is for DEFECTS IN THE PLATFORM ITSELF the run hit or worked around (tools failing, wrong tool output, broken integrations) — they go to the operator's fix backlog, not the task. Environment state the WORKER should have cleaned but may not have is a concrete_catch, not a platform issue.
worth_facting=false when the run was unremarkable — most runs are; do not invent either kind.`

// selfCritiqueCollector captures what the critique needs from the activity
// stream without keeping the whole trail in memory.
type selfCritiqueCollector struct {
	mu       sync.Mutex
	lastText string
	tools    []string
}

func (c *selfCritiqueCollector) observe(a db.Activity) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch a.Kind {
	case "text":
		if strings.TrimSpace(a.Summary) != "" {
			c.lastText = a.Summary
		}
	case "tool_use":
		if len(c.tools) < 40 {
			arg := strings.TrimSpace(a.Detail)
			if arg == "" {
				arg = strings.TrimSpace(a.Summary)
			}
			c.tools = append(c.tools, a.Tool+": "+clipStr(arg, 120))
		}
	}
}

// maybeSelfCritique runs the critique for terminal reasons worth reviewing and
// writes the verdict as a fact on the intent. Async + best-effort.
func (s *Server) maybeSelfCritique(t *Task, intent *db.Node, reason harness.TerminalReason, c *selfCritiqueCollector) {
	if c == nil {
		return
	}
	switch reason {
	case harness.ReasonMaxTurns, harness.ReasonTimeout:
	default:
		return // completed runs are the worker's own wrap-up; aborted runs lack closure
	}
	if getEnv("ARTEX_RSI_SELFCRITIQUE", "1") == "0" {
		return
	}
	c.mu.Lock()
	text, tools := c.lastText, append([]string(nil), c.tools...)
	c.mu.Unlock()
	if strings.TrimSpace(text) == "" && len(tools) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		var b strings.Builder
		fmt.Fprintf(&b, "Final summary:\n%s\n\nTool trail (%d calls):\n%s\n",
			clipStr(text, 2000), len(tools), clipStr(strings.Join(tools, "\n"), 3000))
		out, err := s.oneShotLLM(ctx, selfCritiqueSystem, b.String(), 600)
		if err != nil || strings.TrimSpace(out) == "" {
			return
		}
		var res struct {
			WorthFacting   bool     `json:"worth_facting"`
			Critique       string   `json:"critique"`
			Catches        []string `json:"concrete_catches"`
			PlatformIssues []struct {
				Title  string `json:"title"`
				Detail string `json:"detail"`
			} `json:"platform_issues"`
		}
		if jsonErr := parseStrictJSON(stripFence(strings.TrimSpace(out)), &res); jsonErr != nil {
			return
		}
		for _, iss := range res.PlatformIssues {
			if title := strings.TrimSpace(iss.Title); title != "" {
				s.filePlatformIssue("self-critique", fmt.Sprintf("task:%s intent:%d", t.ID, intent.ID), title, strings.TrimSpace(iss.Detail))
			}
		}
		if !res.WorthFacting {
			return
		}
		body := "[self-critique of exhausted run] " + res.Critique
		for _, cat := range res.Catches {
			body += "\n- catch: " + cat
		}
		payload := map[string]any{
			"summary":  clipStr("self-critique: "+firstSentence(res.Critique), 200),
			"detail":   clipStr(body, 1500),
			"evidence": "post-run critique over the run's final summary and tool trail",
			"source":   "tool_output",
		}
		if _, err := t.Store.AddNode("fact", payload, 0, "origin", "self-critique", nil); err != nil {
			log.Printf("[rsi] self-critique fact write failed on intent %d: %v", intent.ID, err)
		} else {
			log.Printf("[rsi] intent #%d: self-critique fact recorded (%d catch(es))", intent.ID, len(res.Catches))
		}
	}()
}

func firstSentence(s string) string {
	if i := strings.IndexAny(s, ".!\n"); i > 0 {
		return s[:i]
	}
	return s
}

// SetSelfCritique installs the critique callback on the engine.
func (e *Engine) SetSelfCritique(fn func(t *Task, intent *db.Node, reason harness.TerminalReason, c *selfCritiqueCollector)) {
	e.selfCritique = fn
}

// parseStrictJSON is json.Unmarshal with a clearer name at call sites.
func parseStrictJSON(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// srvSelfCritique indirection so the engine keeps compiling without the server.
func (e *Engine) srvSelfCritique(t *Task, intent *db.Node, reason harness.TerminalReason, c *selfCritiqueCollector) {
	if e.selfCritique == nil {
		return
	}
	e.selfCritique(t, intent, reason, c)
}
