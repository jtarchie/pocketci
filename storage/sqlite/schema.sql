CREATE TABLE IF NOT EXISTS tasks (
  id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  path TEXT NOT NULL,
  payload BLOB,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(path)
) STRICT;

CREATE TABLE IF NOT EXISTS pipelines (
  id TEXT NOT NULL PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  content TEXT NOT NULL,
  content_type TEXT NOT NULL DEFAULT '',
  driver TEXT NOT NULL,
  resume_enabled INTEGER NOT NULL DEFAULT 0,
  rbac_expression TEXT NOT NULL DEFAULT '',
  created_at TEXT DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT DEFAULT CURRENT_TIMESTAMP
) STRICT;

CREATE TABLE IF NOT EXISTS pipeline_runs (
  id TEXT NOT NULL PRIMARY KEY,
  pipeline_id TEXT NOT NULL,
  status TEXT NOT NULL,
  started_at TEXT,
  completed_at TEXT,
  error_message TEXT,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (pipeline_id) REFERENCES pipelines(id) ON DELETE CASCADE
) STRICT;

-- FTS5 virtual table for pipeline full-text search (name + content).
CREATE VIRTUAL TABLE IF NOT EXISTS pipelines_fts USING fts5(
  id,
  name,
  content,
  tokenize = 'unicode61'
);

-- FTS5 virtual table for general full-text search over any stored record.
-- content holds ANSI-stripped text extracted from the JSON payload.
CREATE VIRTUAL TABLE IF NOT EXISTS data_fts USING fts5(path, content, tokenize = 'unicode61');

-- Remove FTS entries when a pipeline is deleted.
CREATE TRIGGER IF NOT EXISTS pipelines_fts_delete
AFTER
  DELETE ON pipelines BEGIN
DELETE FROM
  pipelines_fts
WHERE
  id = OLD.id;

END;

-- Remove FTS entries when a task is deleted.
CREATE TRIGGER IF NOT EXISTS data_fts_delete
AFTER
  DELETE ON tasks BEGIN
DELETE FROM
  data_fts
WHERE
  path = OLD.path;

END;

-- FTS5 virtual table for pipeline run search (id, status, error_message).
CREATE VIRTUAL TABLE IF NOT EXISTS pipeline_runs_fts USING fts5(
  id,
  status,
  error_message,
  tokenize = 'unicode61'
);

-- Populate FTS when a run is created.
CREATE TRIGGER IF NOT EXISTS pipeline_runs_fts_insert
AFTER
INSERT
  ON pipeline_runs BEGIN
INSERT INTO
  pipeline_runs_fts(id, status, error_message)
VALUES
  (
    NEW.id,
    NEW.status,
    COALESCE(NEW.error_message, '')
  );

END;

-- Keep FTS in sync when a run's status or error_message changes.
CREATE TRIGGER IF NOT EXISTS pipeline_runs_fts_update
AFTER
UPDATE
  ON pipeline_runs BEGIN
DELETE FROM
  pipeline_runs_fts
WHERE
  rowid IN (
    SELECT
      rowid
    FROM
      pipeline_runs_fts
    WHERE
      id = OLD.id
  );

INSERT INTO
  pipeline_runs_fts(id, status, error_message)
VALUES
  (
    NEW.id,
    NEW.status,
    COALESCE(NEW.error_message, '')
  );

END;

-- Remove FTS entries when a run is deleted.
CREATE TRIGGER IF NOT EXISTS pipeline_runs_fts_delete
AFTER
  DELETE ON pipeline_runs BEGIN
DELETE FROM
  pipeline_runs_fts
WHERE
  rowid IN (
    SELECT
      rowid
    FROM
      pipeline_runs_fts
    WHERE
      id = OLD.id
  );

END;

-- Remove task data stored under .../{namespace}/pipeline/{run_id}/... when a pipeline run is deleted.
-- This fires for each run row cascade-deleted from pipeline_runs (e.g. when deleting a pipeline),
-- and the existing data_fts_delete trigger then cleans the FTS index for each removed task row.
-- The leading '%' matches any namespace prefix prepended by the storage layer.
CREATE TRIGGER IF NOT EXISTS pipeline_runs_tasks_delete
AFTER
  DELETE ON pipeline_runs BEGIN
DELETE FROM
  tasks
WHERE
  path LIKE '%/pipeline/' || OLD.id || '/%';

END;