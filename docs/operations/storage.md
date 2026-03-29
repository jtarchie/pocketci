# Storage

PocketCI persists pipelines, runs, and task data using SQLite. The backend is
configured via flags on the [server](../cli/server.md) command.

## SQLite

SQLite stores all data in a single file with full-text search (FTS5) for
pipeline and run search queries.

| Flag                    | Env                      | Default       | Description                                                                       |
| ----------------------- | ------------------------ | ------------- | --------------------------------------------------------------------------------- |
| `--storage-sqlite-path` | `CI_STORAGE_SQLITE_PATH` | `pocketci.db` | Path to the SQLite database file. Use `:memory:` for ephemeral in-memory storage. |

```bash
pocketci server --storage-sqlite-path pocketci.db
```
