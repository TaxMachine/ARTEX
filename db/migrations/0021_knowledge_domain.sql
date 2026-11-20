-- Knowledge graph domains: exploit-craft knowledge is domain-scoped so agents
-- working web/crypto never wade through heap-grooming knowledge and vice
-- versa. general = cross-domain (tooling, lab environment, methodology).
-- Derived from the vulnclass taxonomy prefixes on insert when not explicit.
ALTER TABLE knowledge_nodes ADD COLUMN IF NOT EXISTS domain TEXT NOT NULL DEFAULT 'general';
CREATE INDEX IF NOT EXISTS idx_knowledge_domain ON knowledge_nodes (domain, kind, updated_at DESC);
