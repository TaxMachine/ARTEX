-- User-chosen display name for a custom LLM API (e.g. "IKHDev"). Shown on the
-- LLM page group header and every model picker's origin tag; presets keep
-- their built-in labels.
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS provider_label TEXT NOT NULL DEFAULT '';
