-- rsi_policies.world_ids: the replay worlds a policy version was evaluated on
-- (NULL/empty = all worlds, the historical behavior). Dreaming can be scoped
-- to a chosen set of tasks; recording the scope keeps a policy's trajectory
-- report and its keep-vs-replace comparison honest (never mixed with rows from
-- a differently-scoped evaluation of the same version).
ALTER TABLE rsi_policies ADD COLUMN IF NOT EXISTS world_ids BIGINT[] NOT NULL DEFAULT '{}';
