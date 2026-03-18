# Secrets

The CI system supports encrypted secrets that are injected into pipeline tasks
as environment variables. Secrets are encrypted at rest using AES-256-GCM, so
they remain inaccessible even if someone has direct access to the underlying
storage.

## How It Works

1. **Storage**: Secrets are stored in an encrypted backend (SQLite or S3). Each
   secret is encrypted with a key derived from a passphrase you provide.
2. **Injection**: Pipelines reference secrets in `env` using a `secret:` prefix.
   At runtime, the system resolves the secret and injects the plaintext value
   into the container's environment.
3. **Redaction**: Any secret values that appear in task stdout or stderr are
   automatically replaced with `***REDACTED***` before being stored or returned.
4. **Fail Fast**: If a pipeline references a secret that doesn't exist, the
   pipeline fails immediately with a clear error message naming the missing key.

## Setting Secrets

Secrets can be set at two scopes: **pipeline** (only visible to one pipeline) or
**global** (visible to all pipelines, used as a fallback).

### Pipeline-Scoped Secrets (`pocketci set-pipeline --secret`)

Pass secrets when uploading a pipeline using `--secret KEY=VALUE` (or
`-e KEY=VALUE`). These are scoped to that pipeline only.

```bash
pocketci set-pipeline pipeline.ts \
  --secret API_KEY=sk-1234567890 \
  --secret DB_PASSWORD=hunter2
```

### Global Secrets

Global secrets are shared across all pipelines. Set them on the server:

```bash
pocketci server \
  --secrets-sqlite-path secrets.db \
  --secrets-sqlite-passphrase change-me-in-production \
  --secret SHARED_TOKEN=tok-global-abc \
  --secret REGISTRY_PASSWORD=ghp-xyz
```

At runtime, the system checks pipeline scope first, then falls back to global.
This means a pipeline can override a global secret with its own value.

### Environment Variables

| Environment Variable              | CLI Flag                         | Description                                             |
| --------------------------------- | -------------------------------- | ------------------------------------------------------- |
| `CI_SECRETS_SQLITE_PATH`          | `--secrets-sqlite-path`          | SQLite database file path                               |
| `CI_SECRETS_SQLITE_PASSPHRASE`    | `--secrets-sqlite-passphrase`    | Encryption passphrase for SQLite backend                |
| `CI_SECRETS_S3_BUCKET`            | `--secrets-s3-bucket`            | S3 bucket name (enables S3 backend when set)            |
| `CI_SECRETS_S3_ENDPOINT`          | `--secrets-s3-endpoint`          | S3-compatible endpoint URL                              |
| `CI_SECRETS_S3_REGION`            | `--secrets-s3-region`            | AWS region                                              |
| `CI_SECRETS_S3_ACCESS_KEY_ID`     | `--secrets-s3-access-key-id`     | S3 access key ID                                        |
| `CI_SECRETS_S3_SECRET_ACCESS_KEY` | `--secrets-s3-secret-access-key` | S3 secret access key                                    |
| `CI_SECRETS_S3_PASSPHRASE`        | `--secrets-s3-passphrase`        | Application-layer AES-256-GCM encryption key            |
| `CI_SECRETS_S3_ENCRYPT`           | `--secrets-s3-encrypt`           | Server-side encryption: `sse-s3`, `sse-kms`, or `sse-c` |
| `CI_SECRETS_S3_PREFIX`            | `--secrets-s3-prefix`            | S3 key prefix                                           |

## Backend Configuration

### SQLite

The SQLite backend stores secrets in a local database file, encrypted with
AES-256-GCM. The encryption key is derived from the passphrase using SHA-256.

| Flag                          | Env                            | Default   | Description                                                             |
| ----------------------------- | ------------------------------ | --------- | ----------------------------------------------------------------------- |
| `--secrets-sqlite-path`       | `CI_SECRETS_SQLITE_PATH`       | `test.db` | Path to the SQLite database file. Use `:memory:` for ephemeral storage. |
| `--secrets-sqlite-passphrase` | `CI_SECRETS_SQLITE_PASSPHRASE` | —         | Passphrase used to derive the AES-256 key                               |

```bash
# File-based storage
pocketci server \
  --secrets-sqlite-path secrets.db \
  --secrets-sqlite-passphrase my-passphrase

# Absolute path
pocketci server \
  --secrets-sqlite-path /var/lib/pocketci/secrets.db \
  --secrets-sqlite-passphrase my-passphrase

# In-memory (useful for testing — secrets don't persist)
pocketci server \
  --secrets-sqlite-path :memory: \
  --secrets-sqlite-passphrase test-key
```

### S3

The S3 backend stores encrypted secrets in an S3-compatible object store. Set
`--secrets-s3-bucket` to enable it; when set it takes precedence over SQLite.

| Flag                             | Env                               | Description                                                       |
| -------------------------------- | --------------------------------- | ----------------------------------------------------------------- |
| `--secrets-s3-bucket`            | `CI_SECRETS_S3_BUCKET`            | S3 bucket name (required to use S3 backend)                       |
| `--secrets-s3-endpoint`          | `CI_SECRETS_S3_ENDPOINT`          | S3-compatible endpoint URL (e.g. `http://localhost:9000`)         |
| `--secrets-s3-region`            | `CI_SECRETS_S3_REGION`            | AWS region                                                        |
| `--secrets-s3-access-key-id`     | `CI_SECRETS_S3_ACCESS_KEY_ID`     | S3 access key ID                                                  |
| `--secrets-s3-secret-access-key` | `CI_SECRETS_S3_SECRET_ACCESS_KEY` | S3 secret access key                                              |
| `--secrets-s3-passphrase`        | `CI_SECRETS_S3_PASSPHRASE`        | Application-layer AES-256-GCM encryption passphrase (recommended) |
| `--secrets-s3-encrypt`           | `CI_SECRETS_S3_ENCRYPT`           | Provider SSE: `sse-s3` (AES-256), `sse-kms` (KMS), or `sse-c`     |
| `--secrets-s3-prefix`            | `CI_SECRETS_S3_PREFIX`            | Key prefix to scope all secrets within the bucket                 |

```bash
# AWS S3 with application-layer encryption
pocketci server \
  --secrets-s3-bucket my-secrets-bucket \
  --secrets-s3-region us-east-1 \
  --secrets-s3-passphrase my-encryption-passphrase

# MinIO (local development)
pocketci server \
  --secrets-s3-bucket secrets \
  --secrets-s3-endpoint http://localhost:9000 \
  --secrets-s3-region us-east-1 \
  --secrets-s3-access-key-id minioadmin \
  --secrets-s3-secret-access-key minioadmin \
  --secrets-s3-passphrase my-encryption-passphrase
```

## Using Secrets in Pipelines

Any string value prefixed with `secret:` is resolved from the secrets backend
before it is used. This works across **task environment variables**, **native
resource configuration**, and **notification config fields**.

### Task Environment Variables

Reference secrets in a task's `env` map:

```typescript
const pipeline = async () => {
  let result = await runtime.run({
    name: "deploy",
    image: "alpine",
    command: {
      path: "sh",
      args: [
        "-c",
        'curl -H "Authorization: Bearer $API_KEY" https://api.example.com',
      ],
    },
    env: {
      API_KEY: "secret:API_KEY", // Resolved from secrets backend
      NODE_ENV: "production", // Plain value, passed as-is
    },
  });
};

export { pipeline };
```

### Native Resource Source and Params

Secret references work in the `source` and `params` maps of native resource
operations (`nativeResources.check`, `.fetch`, `.push`). Nested maps are walked
recursively — only string values with the `secret:` prefix are substituted;
non-string values such as numbers and booleans are left unchanged.

```typescript
const pipeline = async () => {
  // check — source credentials resolved from secrets
  const versions = nativeResources.check({
    type: "git",
    source: {
      uri: "https://github.com/my-org/private-repo.git",
      private_key: "secret:GIT_DEPLOY_KEY",
    },
  });

  // fetch — nested source + params both resolved
  const result = await nativeResources.fetch({
    type: "s3",
    source: {
      bucket: "my-bucket",
      credentials: {
        access_key: "secret:AWS_ACCESS_KEY",
        secret_key: "secret:AWS_SECRET_KEY",
      },
    },
    version: versions.versions[0],
    params: { unpack: true },
    destDir: "/workspace",
  });
};

export { pipeline };
```

### Notification Config Fields

Secret references work in notification backend configuration fields: `token`
(Slack), `webhook` (Teams), `url` (HTTP), and every entry in `headers` (HTTP).
The secret is resolved at the moment `notify.send()` is called, not when
`notify.setConfigs()` is called, so the stored config always uses the `secret:`
prefix string as a placeholder.

```typescript
const pipeline = async () => {
  notify.setConfigs({
    // Slack — token resolved from secrets
    "slack-builds": {
      type: "slack",
      token: "secret:SLACK_BOT_TOKEN",
      channels: ["#builds"],
    },
    // Microsoft Teams — webhook resolved from secrets
    "teams-alerts": {
      type: "teams",
      webhook: "secret:TEAMS_WEBHOOK_URL",
    },
    // HTTP — URL and Authorization header resolved from secrets
    "http-hook": {
      type: "http",
      url: "secret:WEBHOOK_URL",
      method: "POST",
      headers: {
        Authorization: "secret:WEBHOOK_TOKEN",
      },
    },
  });

  await notify.send({ name: "slack-builds", message: "Build started" });
};

export { pipeline };
```

## Scoping

Secrets are scoped to limit access:

- **Pipeline scope** (`pipeline/<id>`): Set via `pocketci run --secret`. Each
  pipeline only sees its own pipeline-scoped secrets.
- **Global scope** (`global`): Set via `pocketci server --secret` or
  `pocketci run --global-secret`. Shared across all pipelines.

The system checks pipeline scope first, then falls back to global. A
pipeline-scoped secret with the same key overrides its global counterpart.

## Output Redaction

Secret values are automatically scrubbed from pipeline output. If a task prints
a secret value to stdout or stderr, it is replaced with `***REDACTED***` before
the output is stored or displayed.

This uses longest-match-first ordering, so if one secret's value is a substring
of another, the longer value is redacted first to avoid partial matches.

## Full Example

```bash
# Start server with SQLite secrets backend and a global secret
pocketci server \
  --secrets-sqlite-path my-secrets.db \
  --secrets-sqlite-passphrase change-me-in-production \
  --secret REGISTRY_TOKEN=ghp-abc123

# Upload pipeline with a pipeline-scoped secret
pocketci set-pipeline examples/both/secrets-basic.ts \
  --server http://localhost:8080 \
  --secret API_KEY=sk-live-abc123
```

## Architecture

The secrets system follows the same pluggable backend pattern as the
`orchestra/` drivers:

```
secrets/
  secrets.go          # Manager interface, Register/New registry
  encryption.go       # AES-256-GCM encryption primitives
  sqlite/
    sqlite.go         # SQLite-backed encrypted backend (self-registers via init())
  s3/
    s3.go             # S3-backed double-encrypted backend (self-registers via init())
```

New backends (e.g., HashiCorp Vault, AWS Secrets Manager) can be added by
implementing the `secrets.Manager` interface and calling `secrets.Register()` in
an `init()` function. See
[implementing-driver](../drivers/implementing-driver.md) for the analogous
pattern used by orchestra drivers.
