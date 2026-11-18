-- Finding categories (operating-systems / hypervisors / web / applications / others).
-- Validated in Go (ValidFindingCategory); the default keeps old rows meaningful.
ALTER TABLE findings ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT 'others';
