-- Primitives vs exploit-chain anatomy on findings: a finding records a
-- CONFIRMED chain (primitives composed into the attacker's goal). The two
-- columns keep the distinction explicit instead of buried in prose:
--   primitives: JSON array of the single weaknesses the chain is built on
--               ({name, evidence, confirmed}) — facts, not findings.
--   chain: the composed exploit chain, one line per step with its state
--          (step, action, result) — every step confirmed for a confirmed
--          finding; an assumed step makes the whole thing a hypothesis.
ALTER TABLE findings
    ADD COLUMN IF NOT EXISTS primitives JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS chain JSONB NOT NULL DEFAULT '[]'::jsonb;
