-- Software projects: one row per target software (clfs.sys, WordPress 7.1.4,
-- win32k…). Tasks link to a project; every task on the same software then
-- cross-references through it — findings, binaries (patch generations), PoCs,
-- and the crossmap function graph all roll up per project.
--
-- key: canonical slug (clfs-sys, wordpress-7-1-4). crossmap binary names and
-- binary assets carry project_key to join back.

CREATE TABLE IF NOT EXISTS projects (
    id          BIGSERIAL PRIMARY KEY,
    key         TEXT NOT NULL UNIQUE,          -- slug
    name        TEXT NOT NULL,                 -- display
    kind        TEXT NOT NULL DEFAULT 'software', -- software | product | suite
    description TEXT NOT NULL DEFAULT '',
    -- crossmap scope: which binary names belong to this project.
    binaries    TEXT[] NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_projects_binaries ON projects USING GIN(binaries);

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS project_id BIGINT REFERENCES projects(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks(project_id) WHERE project_id IS NOT NULL;

-- Binary assets and findings link to a project for rollups.
ALTER TABLE assets ADD COLUMN IF NOT EXISTS project_id BIGINT REFERENCES projects(id) ON DELETE SET NULL;
ALTER TABLE findings ADD COLUMN IF NOT EXISTS project_id BIGINT REFERENCES projects(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_assets_project ON assets(project_id) WHERE project_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_findings_project ON findings(project_id) WHERE project_id IS NOT NULL;