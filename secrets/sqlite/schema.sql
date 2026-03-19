CREATE TABLE IF NOT EXISTS secrets (
  scope           TEXT    NOT NULL,
  key             TEXT    NOT NULL,
  encrypted_value BLOB    NOT NULL,
  version         INTEGER NOT NULL DEFAULT 1,
  updated_at      INTEGER NOT NULL DEFAULT (unixepoch()),
  PRIMARY KEY (scope, key)
) STRICT;

-- Auto-increment version and refresh updated_at on every update.
CREATE TRIGGER IF NOT EXISTS secrets_version_update
AFTER UPDATE ON secrets BEGIN
  UPDATE secrets
  SET version    = OLD.version + 1,
      updated_at = unixepoch()
  WHERE scope = NEW.scope AND key = NEW.key;
END;
