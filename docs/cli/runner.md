# pocketci runner

Execute a pipeline locally in a single iteration.

```bash
pocketci runner <pipeline-file> [options]
```

## Options

- `--driver` — orchestration driver (`docker`, `native`, `k8s`, etc.; default:
  `docker`)
- `--storage-sqlite-path` — SQLite database file path (default: `test.db`, env:
  `CI_STORAGE_SQLITE_PATH`)
- `--secret` — set pipeline-scoped secret (repeatable; format: `KEY=VALUE`)
- `--global-secret` — set global secret (repeatable)
- `--secrets-sqlite-path` — SQLite secrets database file path (env:
  `CI_SECRETS_SQLITE_PATH`)
- `--secrets-sqlite-passphrase` — encryption passphrase for SQLite secrets (env:
  `CI_SECRETS_SQLITE_PASSPHRASE`)
- `--log-level` — log level (`debug`, `info`, `warn`, `error`)
- `--log-format` — log format (`json` or text)

## Example

```bash
pocketci runner examples/both/hello-world.ts --driver native --log-level debug
```

See [Secrets](../operations/secrets.md) for details on secret handling.
