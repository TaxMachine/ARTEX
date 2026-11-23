package server

import (
	"context"
	"database/sql"
	"errors"
	"log"
)

// DreamerDefaultPrompt seeds the Dream-RSI policy-development agent. Editable
// in the Agents page; changes take effect on the next dream cycle (the system
// prompt is read per call).
const DreamerDefaultPrompt = `You are the policy-development agent of a Dream-RSI loop for an autonomous pentest platform.

You are given replay evaluations of exploration policies over recorded discovery
trees (worlds). Each world is a completed task: nodes are intents/facts/findings,
scores are exploit value (0..1), costs are recorded LLM spend. A policy decides
which frontier nodes to expand per round and when to stop; rho sweeps budget
preference. V is the quality-per-cost hypervolume minus a parallelism penalty.

Your job: choose the policy to deploy next round. You may:
1. pick one of the evaluated candidates (the incumbent is always available —
   keeping it is monotonically safe),
2. propose a K variant of an evaluated family (greedy-K / depth-K with a
   different K, 1..6) that was NOT evaluated — but only with a specific
   trajectory-grounded reason why the trend suggests it.

Output STRICT JSON only:
{"keep": true|false, "code": "<dsl>", "reason": "<2-4 sentences, cite the numbers>"}
"keep": true means keep the incumbent (then code = incumbent's code).
DSL grammar: seed | rho-greedy | greedy-K | depth-K  (K in 1..6)`

// seedDreamerAgent registers the Dreamer agent row (configurable model via the
// Agents page llm_profile binding) and its default prompt, first-insert-only.
func (s *Server) seedDreamerAgent() {
	const flag = "dreamer_agent_seed_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	tx, err := s.m.pg.BeginTx(context.Background(), nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SELECT pg_advisory_xact_lock(7337741024)`); err != nil {
		return
	}
	var done string
	err = tx.QueryRow(`SELECT value FROM settings WHERE key=$1`, flag).Scan(&done)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return
	}
	if done == "true" {
		return
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO agents(key,name,description,role,builtin,enabled,reasoning_effort)
	VALUES ('dreamer','Dreamer (Dream-RSI)','The Dream-RSI policy-development agent: reviews replay evaluations of exploration policies over recorded task history and picks/proposes the next deployed policy with rationale. Configure its model here (LLM profile binding); runs one-shot per dream cycle.','assistant',false,true,'high')
	ON CONFLICT (key) DO UPDATE SET description=EXCLUDED.description RETURNING id`).Scan(&id)
	if err != nil {
		return
	}
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM agent_prompts WHERE agent_id=$1`, id).Scan(&count); err != nil {
		return
	}
	if count == 0 {
		var pid int64
		if err = tx.QueryRow(`INSERT INTO agent_prompts(agent_id,version,template_text,note,updated_by)
			VALUES ($1,1,$2,'built-in default','system') RETURNING id`, id, DreamerDefaultPrompt).Scan(&pid); err != nil {
			return
		}
		if _, err = tx.Exec(`UPDATE agents SET current_prompt_id=$1 WHERE id=$2`, pid, id); err != nil {
			return
		}
	}
	if _, err = tx.Exec(`INSERT INTO settings(key,value) VALUES ($1,'true') ON CONFLICT (key) DO UPDATE SET value='true'`, flag); err != nil {
		return
	}
	if err = tx.Commit(); err != nil {
		return
	}
	log.Printf("[agents] dreamer seeded (Dream-RSI policy development, configurable via Agents page)")
}
