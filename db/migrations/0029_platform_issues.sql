-- Platform issues: defects in ARTEX itself (broken tools, leaked state,
-- wrong behavior) surfaced by the self-improvement loops — kept OUT of
-- rsi_lessons (which carries exploration guidance into worker prompts) so
-- operator work items have their own reviewable backlog.
CREATE TABLE IF NOT EXISTS platform_issues (
    id BIGSERIAL PRIMARY KEY,
    title TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,              -- self-critique | contrastive | triage-critique | manual | budget-gate
    source_ref TEXT NOT NULL DEFAULT '',-- e.g. "task:10 intent:1184"
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','wontfix')),
    note TEXT NOT NULL DEFAULT '',      -- resolution note
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_platform_issues_status ON platform_issues (status, created_at DESC);
