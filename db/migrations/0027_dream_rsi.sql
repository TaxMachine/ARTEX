-- Dream-RSI: recursive self-improvement over recorded exploration history
-- (arXiv 2609.14858, adapted). Phases:
--   1. Scored history: node/edge exports already exist in exploration_*;
--      rsi_node_scores attaches a scalar quality to every node (the fixed
--      evaluator's verdict, computed once, immutable).
--   2. Replay worlds: rsi_worlds snapshots a completed task's exploration
--      tree (+scores+costs) as a deterministic replay surface.
--   3. Policies: rsi_policies versions executable policy code (ordering /
--      parallelism / stopping for intents). The current planner behavior is
--      policy v0.
--   4. The loop: rsi_evaluations stores replay scores per (policy, world);
--      the best candidate redeploys through the operator approval gate.

CREATE TABLE IF NOT EXISTS rsi_node_scores (
    id BIGSERIAL PRIMARY KEY,
    exploration_id BIGINT NOT NULL,
    node_id BIGINT NOT NULL,
    score DOUBLE PRECISION NOT NULL DEFAULT 0,
    quality DOUBLE PRECISION NOT NULL DEFAULT 0,   -- normalized 0..1 component
    cost DOUBLE PRECISION NOT NULL DEFAULT 0,      -- tokens + wall-clock units
    components JSONB NOT NULL DEFAULT '{}'::jsonb, -- how the score was derived
    policy_version INT NOT NULL DEFAULT 0,          -- evaluator version
    computed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (node_id, policy_version)
);
CREATE INDEX IF NOT EXISTS idx_rsi_scores_explore ON rsi_node_scores (exploration_id);

CREATE TABLE IF NOT EXISTS rsi_worlds (
    id BIGSERIAL PRIMARY KEY,
    task_id BIGINT NOT NULL,
    name TEXT NOT NULL,
    tree JSONB NOT NULL,            -- {nodes: [{id, kind, parent, score, cost, at}], root}
    stats JSONB NOT NULL DEFAULT '{}'::jsonb, -- B_i, S_i, depth, node counts
    source_policy INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (task_id, name)
);

CREATE TABLE IF NOT EXISTS rsi_policies (
    id BIGSERIAL PRIMARY KEY,
    version INT NOT NULL,
    code TEXT NOT NULL,             -- executable Go expr policy (sandboxed DSL)
    note TEXT NOT NULL DEFAULT '',  -- what the development agent changed + why
    origin TEXT NOT NULL DEFAULT 'seed', -- seed | dreamed
    avg_score DOUBLE PRECISION,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (version)
);

CREATE TABLE IF NOT EXISTS rsi_evaluations (
    id BIGSERIAL PRIMARY KEY,
    policy_version INT NOT NULL,
    world_id BIGINT NOT NULL,
    rho DOUBLE PRECISION NOT NULL,
    calls INT NOT NULL,             -- n_{i,rho} replayed agent calls
    rounds INT NOT NULL,            -- k* completed rounds
    best_score DOUBLE PRECISION,    -- s_{i,rho} revealed
    pareto JSONB NOT NULL DEFAULT '[]'::jsonb,
    score DOUBLE PRECISION NOT NULL DEFAULT 0, -- V_i^m
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (policy_version, world_id, rho)
);
CREATE INDEX IF NOT EXISTS idx_rsi_eval_policy ON rsi_evaluations (policy_version);
