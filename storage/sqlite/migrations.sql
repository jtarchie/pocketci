-- Idempotent migrations for existing databases.
-- Each statement is a no-op if the column already exists (SQLite returns
-- "duplicate column name" which is silently ignored).

ALTER TABLE pipelines ADD COLUMN content_type TEXT NOT NULL DEFAULT '';
ALTER TABLE pipelines ADD COLUMN resume_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pipelines ADD COLUMN rbac_expression TEXT NOT NULL DEFAULT '';

-- Ensure all existing task payloads are stored in binary JSONB format.
-- jsonb() on already-JSONB data is a no-op, so this is idempotent.
UPDATE tasks SET payload = jsonb(payload) WHERE payload IS NOT NULL
