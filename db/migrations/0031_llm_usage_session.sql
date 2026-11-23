-- llm_usage.session: the raw recorder session string (exp<N>-<role>[-i<intent>],
-- exp<N>-main[-s<seg>], conv-<id>) so per-request input sizes are attributable
-- per UI session (worker intent, main-agent segment, planner, conversation).
-- The activity table's kind='usage' rows are cumulative run totals and cannot
-- serve as the per-request context-fill signal.
ALTER TABLE llm_usage ADD COLUMN IF NOT EXISTS session TEXT NOT NULL DEFAULT '';

-- Backfill only what is unambiguously attributable from the recorded columns:
-- planner / goals / compactor are one lane per exploration. Worker rows (no
-- intent id recorded) and per-segment main rows stay '' — their history is not
-- recoverable; new rows carry the session from the recorder.
UPDATE llm_usage SET session = 'exp' || exploration_id || '-' || worker
WHERE session = '' AND exploration_id > 0 AND worker IN ('planner', 'goals', 'compactor');

CREATE INDEX IF NOT EXISTS idx_llm_usage_session ON llm_usage(session);
