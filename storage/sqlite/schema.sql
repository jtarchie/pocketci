CREATE TABLE IF NOT EXISTS tasks (
  id            INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  path          TEXT    NOT NULL,
  run_id        TEXT,
  status        TEXT,
  started_at    TEXT,
  elapsed       TEXT,
  error_message TEXT,
  error_type    TEXT,
  payload       BLOB,
  created_at    INTEGER NOT NULL DEFAULT (unixepoch()),
  UNIQUE(path)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_tasks_run_id ON tasks(run_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status  ON tasks(status);

CREATE TABLE IF NOT EXISTS pipelines (
  id              TEXT    NOT NULL PRIMARY KEY,
  name            TEXT    NOT NULL UNIQUE,
  content         TEXT    NOT NULL,
  content_type    TEXT    NOT NULL DEFAULT '',
  driver          TEXT    NOT NULL,
  resume_enabled  INTEGER NOT NULL DEFAULT 0,
  paused          INTEGER NOT NULL DEFAULT 0,
  rbac_expression TEXT    NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
  updated_at      INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;

CREATE TABLE IF NOT EXISTS pipeline_runs (
  id            TEXT    NOT NULL PRIMARY KEY,
  pipeline_id   TEXT    NOT NULL,
  status        TEXT    NOT NULL,
  started_at    INTEGER,
  completed_at  INTEGER,
  error_message TEXT,
  trigger_type  TEXT    NOT NULL DEFAULT '',
  triggered_by  TEXT    NOT NULL DEFAULT '',
  trigger_input TEXT    NOT NULL DEFAULT '{}',
  created_at    INTEGER NOT NULL DEFAULT (unixepoch()),
  FOREIGN KEY (pipeline_id) REFERENCES pipelines(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX IF NOT EXISTS idx_pipeline_runs_pipeline_id ON pipeline_runs(pipeline_id);
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_status      ON pipeline_runs(status);
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_created_at  ON pipeline_runs(created_at);

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
AFTER DELETE ON pipelines BEGIN
  DELETE FROM pipelines_fts WHERE id = OLD.id;
END;

-- Remove FTS entries when a task is deleted.
CREATE TRIGGER IF NOT EXISTS data_fts_delete
AFTER DELETE ON tasks BEGIN
  DELETE FROM data_fts WHERE path = OLD.path;
END;

-- FTS5 virtual table for pipeline run search using external content table.
-- Avoids duplicating data; triggers keep it in sync using the rowid of pipeline_runs.
CREATE VIRTUAL TABLE IF NOT EXISTS pipeline_runs_fts USING fts5(
  id,
  status,
  error_message,
  trigger_type,
  triggered_by,
  content     = pipeline_runs,
  content_rowid = rowid,
  tokenize    = 'unicode61'
);

-- Populate FTS when a run is created.
CREATE TRIGGER IF NOT EXISTS pipeline_runs_fts_insert
AFTER INSERT ON pipeline_runs BEGIN
  INSERT INTO pipeline_runs_fts(rowid, id, status, error_message, trigger_type, triggered_by)
  VALUES (NEW.rowid, NEW.id, NEW.status, COALESCE(NEW.error_message, ''), NEW.trigger_type, NEW.triggered_by);
END;

-- Keep FTS in sync when a run's status or error_message changes.
CREATE TRIGGER IF NOT EXISTS pipeline_runs_fts_update
AFTER UPDATE ON pipeline_runs BEGIN
  INSERT INTO pipeline_runs_fts(pipeline_runs_fts, rowid, id, status, error_message, trigger_type, triggered_by)
  VALUES ('delete', OLD.rowid, OLD.id, OLD.status, COALESCE(OLD.error_message, ''), OLD.trigger_type, OLD.triggered_by);
  INSERT INTO pipeline_runs_fts(rowid, id, status, error_message, trigger_type, triggered_by)
  VALUES (NEW.rowid, NEW.id, NEW.status, COALESCE(NEW.error_message, ''), NEW.trigger_type, NEW.triggered_by);
END;

-- Remove FTS entries when a run is deleted.
CREATE TRIGGER IF NOT EXISTS pipeline_runs_fts_delete
AFTER DELETE ON pipeline_runs BEGIN
  INSERT INTO pipeline_runs_fts(pipeline_runs_fts, rowid, id, status, error_message, trigger_type, triggered_by)
  VALUES ('delete', OLD.rowid, OLD.id, OLD.status, COALESCE(OLD.error_message, ''), OLD.trigger_type, OLD.triggered_by);
END;

-- Remove task data stored under .../{namespace}/pipeline/{run_id}/... when a pipeline run is deleted.
-- Uses the indexed run_id column instead of a leading-wildcard LIKE scan.
CREATE TRIGGER IF NOT EXISTS pipeline_runs_tasks_delete
AFTER DELETE ON pipeline_runs BEGIN
  DELETE FROM tasks WHERE run_id = OLD.id;
END;

-- Schedules: stores schedule triggers for pipelines.
-- Each schedule is tied to a pipeline and optionally targets a specific job.
-- ClaimDueSchedules uses UPDATE...RETURNING for atomic multi-instance safety.
CREATE TABLE IF NOT EXISTS schedules (
  id            TEXT    NOT NULL PRIMARY KEY,
  pipeline_id   TEXT    NOT NULL,
  name          TEXT    NOT NULL,
  schedule_type TEXT    NOT NULL,
  schedule_expr TEXT    NOT NULL,
  job_name      TEXT    NOT NULL DEFAULT '',
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_run_at   INTEGER,
  next_run_at   INTEGER,
  created_at    INTEGER NOT NULL DEFAULT (unixepoch()),
  updated_at    INTEGER NOT NULL DEFAULT (unixepoch()),
  UNIQUE(pipeline_id, name),
  FOREIGN KEY (pipeline_id) REFERENCES pipelines(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX IF NOT EXISTS idx_schedules_next_run
  ON schedules(next_run_at) WHERE enabled = 1;
CREATE INDEX IF NOT EXISTS idx_schedules_pipeline_id ON schedules(pipeline_id);

-- Webhook deduplication: stores truncated SHA-256 hashes of evaluated dedup keys.
-- The (pipeline_id, key_hash) primary key prevents duplicate inserts efficiently.
-- ON DELETE CASCADE removes entries when a pipeline is deleted.
CREATE TABLE IF NOT EXISTS webhook_dedup (
  pipeline_id TEXT    NOT NULL,
  key_hash    BLOB    NOT NULL,
  created_at  INTEGER NOT NULL DEFAULT (unixepoch()),
  PRIMARY KEY (pipeline_id, key_hash),
  FOREIGN KEY (pipeline_id) REFERENCES pipelines(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX IF NOT EXISTS idx_webhook_dedup_created_at ON webhook_dedup(created_at);
