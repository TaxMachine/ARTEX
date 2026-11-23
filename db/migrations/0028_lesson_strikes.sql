-- Self-improvement loop bookkeeping: strike counter for the outcome-feedback
-- rule (a lesson shown in N zero-finding tasks retires), complementing the
-- existing candidate/adopted/retired status machine.
ALTER TABLE rsi_lessons ADD COLUMN IF NOT EXISTS retire_strikes INTEGER NOT NULL DEFAULT 0;
