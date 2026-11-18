-- Native machines: first-class VM/SSH targets, machine credentials and evidence.
--
-- task_credentials grows a scope beyond web logins: protocol (web|ssh|winrm),
-- port, and either a password or an SSH private key (both encrypted at rest by
-- db/secrets.go). domain stays "host/IP"; web rows keep protocol='web', port=0.

ALTER TABLE task_credentials
    ADD COLUMN IF NOT EXISTS protocol    TEXT    NOT NULL DEFAULT 'web',
    ADD COLUMN IF NOT EXISTS port        INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS secret_type TEXT    NOT NULL DEFAULT 'password',
    ADD COLUMN IF NOT EXISTS private_key TEXT    NOT NULL DEFAULT '';

-- The old uniqueness ignored protocol/port, so an SSH credential and a web
-- login for the same host+user could not coexist. Replace it.
ALTER TABLE task_credentials
    DROP CONSTRAINT IF EXISTS task_credentials_task_id_domain_username_key;
DROP INDEX IF EXISTS task_credentials_task_id_domain_username_key;

CREATE UNIQUE INDEX IF NOT EXISTS task_credentials_scope_idx
    ON task_credentials (task_id, protocol, domain, port, username);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'task_credentials_protocol_check') THEN
        ALTER TABLE task_credentials
            ADD CONSTRAINT task_credentials_protocol_check
            CHECK (protocol IN ('web', 'ssh', 'winrm'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'task_credentials_secret_check') THEN
        ALTER TABLE task_credentials
            ADD CONSTRAINT task_credentials_secret_check
            CHECK (secret_type IN ('password', 'key'));
    END IF;
END $$;

-- machines: operator-registered targets. Agents may only address rows here by
-- name; nothing in the agent input is used as a libvirt domain or script path.
--   kind=libvirt  -> domain + optional host/ssh_port (bridge-networked VM)
--   kind=script   -> start_script/stop_script (raw QEMU lab VMs like v424)
--   kind=ssh      -> host/ssh_port only (no lifecycle)
CREATE TABLE IF NOT EXISTS machines (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    description  TEXT NOT NULL DEFAULT '',
    os           TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL DEFAULT 'ssh',
    host         TEXT NOT NULL DEFAULT '',
    ssh_port     INTEGER NOT NULL DEFAULT 22,
    domain       TEXT NOT NULL DEFAULT '',
    start_script TEXT NOT NULL DEFAULT '',
    stop_script  TEXT NOT NULL DEFAULT '',
    workdir      TEXT NOT NULL DEFAULT '',
    data_dir     TEXT NOT NULL DEFAULT '',
    protected    BOOLEAN NOT NULL DEFAULT false,
    tags         JSONB NOT NULL DEFAULT '[]',
    config       JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'machines_kind_check') THEN
        ALTER TABLE machines
            ADD CONSTRAINT machines_kind_check
            CHECK (kind IN ('libvirt', 'script', 'ssh'));
    END IF;
END $$;

-- machine_events: the machine equivalent of captured HTTP traffic. One row per
-- SSH exec/upload/download, VM lifecycle action, or pcap capture action. Kept
-- append-only; findings bind to rows through finding_machine_bindings.
CREATE TABLE IF NOT EXISTS machine_events (
    id            BIGSERIAL PRIMARY KEY,
    task_id       BIGINT,
    machine_id    BIGINT REFERENCES machines(id) ON DELETE SET NULL,
    machine       TEXT NOT NULL DEFAULT '',
    protocol      TEXT NOT NULL DEFAULT 'ssh',   -- ssh | vm | pcap
    action        TEXT NOT NULL,                 -- exec|upload|download|start|stop|reset|status|capture_start|capture_stop
    username      TEXT NOT NULL DEFAULT '',
    credential_id BIGINT REFERENCES task_credentials(id) ON DELETE SET NULL,
    command       TEXT NOT NULL DEFAULT '',
    stdout        TEXT NOT NULL DEFAULT '',
    stderr        TEXT NOT NULL DEFAULT '',
    exit_code     INTEGER,
    duration_ms   INTEGER NOT NULL DEFAULT 0,
    bytes         BIGINT NOT NULL DEFAULT 0,
    pcap_path     TEXT NOT NULL DEFAULT '',
    meta          JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS machine_events_task_idx
    ON machine_events (task_id, created_at DESC);
CREATE INDEX IF NOT EXISTS machine_events_machine_idx
    ON machine_events (machine, created_at DESC);
CREATE INDEX IF NOT EXISTS machine_events_action_idx
    ON machine_events (protocol, action);

-- pcap captures started through SSH (Linux targets). One row per capture so a
-- long run can be stopped and downloaded later by id.
CREATE TABLE IF NOT EXISTS machine_captures (
    id          BIGSERIAL PRIMARY KEY,
    machine_id  BIGINT REFERENCES machines(id) ON DELETE CASCADE,
    task_id     BIGINT,
    iface       TEXT NOT NULL DEFAULT 'any',
    bpf_filter  TEXT NOT NULL DEFAULT '',
    remote_path TEXT NOT NULL DEFAULT '',
    local_path  TEXT NOT NULL DEFAULT '',
    pid_file    TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'running', -- running|stopped|failed
    bytes       BIGINT NOT NULL DEFAULT 0,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    stopped_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS machine_captures_machine_idx
    ON machine_captures (machine_id, started_at DESC);

-- Findings can cite machine evidence exactly like traffic. role mirrors the
-- traffic roles so the reporter prompt and UI can reuse the vocabulary.
CREATE TABLE IF NOT EXISTS finding_machine_bindings (
    id         BIGSERIAL PRIMARY KEY,
    finding_id BIGINT NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
    event_id   BIGINT NOT NULL REFERENCES machine_events(id) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT 'supporting',
    note       TEXT NOT NULL DEFAULT '',
    position   INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (finding_id, event_id)
);

CREATE INDEX IF NOT EXISTS finding_machine_bindings_order_idx
    ON finding_machine_bindings (finding_id, position, id);