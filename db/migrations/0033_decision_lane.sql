-- Decision lane: recorded adjudications from the decision model, factored out
-- of the generation roles. scope = finding_adjudication | retest_verdict |
-- finding_dedupe; ref_id = finding id (or retest conversation id for retest
-- scope); agent_value = what the working agent claimed, model_decision = the
-- adjudicated answer. Disagreements are surfaced as task activity, never
-- silently applied.
CREATE TABLE IF NOT EXISTS decision_lane (
	id               BIGSERIAL PRIMARY KEY,
	scope            TEXT NOT NULL,
	ref_id           BIGINT NOT NULL,
	task_id          TEXT,
	question         TEXT NOT NULL DEFAULT '',
	agent_value      TEXT,
	model_decision   TEXT NOT NULL,
	model_reason     TEXT,
	model_confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
	profile_id       BIGINT,
	agree            BOOLEAN NOT NULL,
	created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_decision_lane_scope_ref ON decision_lane(scope, ref_id);
CREATE INDEX IF NOT EXISTS idx_decision_lane_task ON decision_lane(task_id);
