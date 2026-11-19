-- Provider categorization for LLM profiles: which preset (or custom) a profile
-- was created from. Powers the grouped list and the connect flow on the LLM
-- page. 'custom' = manually added. Empty (legacy rows) is treated as custom.
ALTER TABLE llm_profiles
    ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 'custom';
