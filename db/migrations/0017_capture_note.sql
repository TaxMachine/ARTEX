
-- Capture failure reason surfaced in the packet-captures page (why a capture
-- shows failed / 0 bytes).
ALTER TABLE machine_captures
    ADD COLUMN IF NOT EXISTS note TEXT NOT NULL DEFAULT '';
