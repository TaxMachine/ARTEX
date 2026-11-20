-- PoC packaging: every finding carries a ready-to-run, downloadable PoC zip.
-- poc_status: none (default; report_finding leaves it here and the result text
-- points at poc_package) | pending (package building) | ready | failed.
-- poc_path: the built zip's location under data/evidence/poc/.
-- poc_note: what the package contains / why building failed (agent-visible).
ALTER TABLE findings ADD COLUMN IF NOT EXISTS poc_status TEXT NOT NULL DEFAULT 'none';
ALTER TABLE findings ADD COLUMN IF NOT EXISTS poc_path TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN IF NOT EXISTS poc_note TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_findings_poc ON findings (poc_status);
