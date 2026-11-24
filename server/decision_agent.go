package server

import (
	"context"
	"database/sql"
	"errors"
	"log"
)

// seedDecisionAgent registers the Decision agent row (adjudication lane,
// configurable model via the Agents page llm_profile binding) and its default
// prompt, first-insert-only — same pattern as the dreamer seed.
func (s *Server) seedDecisionAgent() {
	const flag = "decision_agent_seed_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	tx, err := s.m.pg.BeginTx(context.Background(), nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SELECT pg_advisory_xact_lock(7337741025)`); err != nil {
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
	VALUES ('decision','Decision (adjudication)','Independent adjudicator for high-volume fixed-choice calls: finding severity/exploit-class honesty, retest verdicts, duplicate detection. Binds a decision model (e.g. gpt-6-luna-decisions) via the LLM profile here; runs one-shot per record, disagreements are surfaced as task activity and never auto-applied.','assistant',false,true,'low')
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
			VALUES ($1,1,$2,'built-in default','system') RETURNING id`, id, DecisionDefaultPrompt).Scan(&pid); err != nil {
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
	log.Printf("[agents] decision seeded (adjudication lane, configurable via Agents page)")
}
