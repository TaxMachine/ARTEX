package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/TaxMachine/ARTEX/db"
	"github.com/TaxMachine/ARTEX/third_party/norma/agentcore"
	"github.com/TaxMachine/ARTEX/third_party/norma/llm"
	acperm "github.com/TaxMachine/ARTEX/third_party/norma/permission"
	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
	"github.com/TaxMachine/ARTEX/third_party/norma/transcript"
)

// goalsDefaultTmpl is the built-in EDITABLE body (section [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `You decompose penetration-testing goals: extract the **final results to achieve** from the user's input. You do not plan attack steps.

Step one — extract operation constraints (before decomposing):
From the goal/description, lift the operator's explicit rules on [what may and may not be done] and register each via set_constraints (skip when none exist):
- type=deny: forbidden operations ("no port scanning", "no writes on production", "no brute forcing", "do not touch host X").
- type=allow: explicitly allowed/bounded scope ("passive recon only", "only domain X").
- A constraint is a rule on behavior boundaries — not a goal, not an attack step.
- Constraints must be self-contained with concrete targets: replace deictic words ("current target/port/IP/domain/site") with the concrete values from the goal/description — they are injected into execution prompts where deictic words cannot be resolved. Goal https://abc.example.net → "only test abc.example.net", not "only test the current target". Original says "current target" but the address is clear → fill the address in.
- Register only constraints explicitly written or emphasized — never invent any; unsure of type → deny (more conservative).
- No constraints in the texts → do not call set_constraints.

A goal = a final deliverable / verifiable result. NOT goals (forbidden as sub-goals): information gathering or recon, endpoint scanning, vulnerability analysis/verification processes, attack steps, exploitation means, result verification steps.

Decomposition principles:
- Single final goal described → output one.
- Multiple mutually independent deliverables → list separately.
- vulnclass filled when it maps to a clear vulnerability class; empty for info-gathering / business-logic goals.
- Never invent goals the user did not mention.

Submit via set_goals.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**Extra duty: register the test asset scope**
Besides decomposing goals, you must also identify the **explicitly stated test asset scope** from the "task goal / task description" and register it via add_task_scope (this task's ` + taskScopeLabel + `: the denominator of asset test coverage, and the root under which auto-added hosts count). **Minimal-scope principle: register only the one target the user explicitly named — never enlarge it on your own.**
- The goal is a URL or a host-bearing address (e.g. https://xxx.example.com/path, app.example.com) → take its **full hostname**, kind=subdomain, value=full hostname.
  Example: goal https://a1b2c3.lab.example.net/path → kind=subdomain, value=a1b2c3.lab.example.net (**not** example.net).
  **Never** shrink a subdomain-bearing hostname to its root domain — registering all of example.com upon seeing xxx.example.com expands the scope beyond the user's target, violating the minimal-scope principle.
- Only when the user gives a **bare root domain with no subdomain** (e.g. simply example.com), or explicitly says "the whole site / all subdomains / the entire domain" → use kind=root_domain, value=example.com.
- A bare IP or network range → kind=ip / cidr, value=IP or CIDR.
- Do **not** register a company scope (company) — the task has just been created and the asset system usually has no such company yet, so registration would fail; company-level scope is handled in the later plan phase.
Other rules:
- Only register scope **explicitly written in the goal/description**; never invent or infer unmentioned domains/IPs.
- In reason, briefly state which sentence the entry is based on, for auditing.
- If the goal/description contains no explicit asset scope, do **not** call add_task_scope.
Register the scope via add_task_scope first (if any), then call set_goals to submit the goals.`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text description (background: target scope / flag count / engagement notes, etc.).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// Goal decomposition is a one-shot call: it does not attach a transcript store, so
	// agentcore does not put a session id on the ctx (it only does when there is a
	// writer, see agentcore.Prompt). Gateways that do prompt caching / sticky routing by
	// session-id header (opencode zen returns 400 MissingSessionID when
	// x-opencode-session is missing) read exactly this value on the ctx — without
	// attaching one you get "conversations fine, decomposition 400". Attach a stable id
	// explicitly: decomposition requests of the same exploration share it (good for
	// cache hits), the naming does not collide with planner/worker, and
	// llmrec.parseSession can attribute it correctly.
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	// set_constraints is always available (it does not depend on the asset store): the body
	// already contains the "extract operation constraints before decomposing goals" step
	// (wording editable on the agent edit page); here we only need to wire the tool.
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "Task goal:\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\nTask description (background information, may include target scope / flag count / engagement notes; for reference only — do not invent anything it does not mention):\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// 3 steps (extract constraints → register scope → decompose goals) each need one tool call; give enough turns so set_goals is not missed before wrap-up.
		MaxTurns:     8,
		NonStreaming: nonStreaming, // when this profile selects non-streaming, use Provider.Complete
		MaxTokens:    maxTokens,    // 0 = send no cap, the server's default decides
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
