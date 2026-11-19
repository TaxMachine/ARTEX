-- Per-agent reasoning effort: overrides the LLM profile's effort for every
-- request this agent makes ('' = inherit the profile; low/medium/high/... =
-- force). Lets e.g. the worker run at high effort while the planner rides the
-- profile default on the same shared provider.
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS reasoning_effort TEXT NOT NULL DEFAULT '';
