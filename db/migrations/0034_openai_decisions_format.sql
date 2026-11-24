-- openai-decisions: a dedicated decision-model format (IKHDev-style
-- POST /v1/decisions question API: choice questions with probability
-- distributions and predicate questions). These models serve no chat endpoint,
-- so they must be representable as their own profile format — and excluded
-- from every chat-facing path (the decision lane has a typed client).
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_format_check;
ALTER TABLE llm_profiles ADD CONSTRAINT llm_profiles_format_check
	CHECK (format = ANY (ARRAY['openai'::text, 'anthropic'::text, 'openai-responses'::text, 'ollama'::text, 'openai-decisions'::text]));
