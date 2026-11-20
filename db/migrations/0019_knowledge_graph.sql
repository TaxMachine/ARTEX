-- Knowledge graph for exploit craft: entities (primitives, techniques,
-- crash signatures, binaries, functions) + typed edges, seeded from and linked
-- to the existing tables (findings, rsi_lessons, crossmap). This is the
-- agent-facing store: rows in PG, queried via tools, exported to an
-- Obsidian-compatible vault for humans.
CREATE TABLE IF NOT EXISTS knowledge_nodes (
    id BIGSERIAL PRIMARY KEY,
    kind TEXT NOT NULL,              -- primitive | technique | crash_signature | binary | function | environment | lesson
    name TEXT NOT NULL,              -- short title
    body TEXT NOT NULL DEFAULT '',   -- markdown: description, constraints, results
    refs JSONB NOT NULL DEFAULT '{}'::jsonb,  -- typed links to existing rows: {"finding_id": 14, "binary": "EXCEL.EXE", "function": "..."}
    tags TEXT[] NOT NULL DEFAULT '{}',
    dedupe_key TEXT NOT NULL,        -- stable identity (e.g. "crash:EXCEL.EXE+c0000005+0x76a9d9")
    source TEXT NOT NULL DEFAULT 'agent',  -- agent | human | system
    task_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (dedupe_key)
);
CREATE INDEX IF NOT EXISTS idx_knowledge_kind ON knowledge_nodes (kind, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_knowledge_tags ON knowledge_nodes USING gin (tags);
CREATE INDEX IF NOT EXISTS idx_knowledge_refs ON knowledge_nodes USING gin (refs jsonb_path_ops);

CREATE TABLE IF NOT EXISTS knowledge_edges (
    id BIGSERIAL PRIMARY KEY,
    src BIGINT NOT NULL REFERENCES knowledge_nodes(id) ON DELETE CASCADE,
    dst BIGINT NOT NULL REFERENCES knowledge_nodes(id) ON DELETE CASCADE,
    rel TEXT NOT NULL,               -- found_in | yields | mitigates | variant_of | requires | evolves_to | reproduces
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (src, dst, rel)
);
CREATE INDEX IF NOT EXISTS idx_knowledge_edges_src ON knowledge_edges (src);
CREATE INDEX IF NOT EXISTS idx_knowledge_edges_dst ON knowledge_edges (dst);
