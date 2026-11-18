-- Which machines a task depends on. Written when a task uses a machine through
-- the native tools; read at ARTEX startup to boot only the VMs that active tasks
-- actually used (never the whole registry). autostart is per dependency so an
-- operator can exclude a machine without changing the machine row.
CREATE TABLE IF NOT EXISTS task_machines (
    task_id    BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    machine_id BIGINT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    autostart  BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, machine_id)
);

CREATE INDEX IF NOT EXISTS task_machines_machine_idx ON task_machines (machine_id);