SET search_path TO admont_ai;

ALTER TABLE llm_providers ADD COLUMN region TEXT NOT NULL DEFAULT '';
