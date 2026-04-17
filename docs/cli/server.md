# pocketci server

Start an HTTP server that manages and executes pipelines.

```bash
pocketci server [options]
```

## Options

- `--port` — HTTP port (default: `8080`, env: `CI_PORT`)
- `--driver` — default orchestration driver (env: `CI_DRIVER`)
- `--allowed-drivers` — comma-separated list of drivers to allow (default: `*`,
  env: `CI_ALLOWED_DRIVERS`)
- `--allowed-features` — comma-separated list of feature gates to enable
  (default: `*`, env: `CI_ALLOWED_FEATURES`)
- `--secret` — set global secret, repeatable (`KEY=VALUE` format)
- `--max-in-flight` — maximum concurrent pipeline executions (default: `10`,
  env: `CI_MAX_IN_FLIGHT`)
- `--max-queue-size` — maximum queued pipeline executions; 0 disables queuing
  (default: `100`, env: `CI_MAX_QUEUE_SIZE`). See
  [Execution Queue](../operations/execution-queue.md)
- `--webhook-timeout` — time allowed for `http.respond()` in webhooks (default:
  `5s`, env: `CI_WEBHOOK_TIMEOUT`)
- `--fetch-timeout` — default timeout for `fetch()` in pipelines (default:
  `30s`, env: `CI_FETCH_TIMEOUT`)
- `--fetch-max-response-mb` — max response body size for `fetch()` in MB
  (default: `10`, env: `CI_FETCH_MAX_RESPONSE_MB`)
- `--max-workdir-mb` — max **decompressed** workdir upload size in MB for
  `pocketci pipeline run` (default: `1024`, env: `CI_MAX_WORKDIR_MB`). The HTTP
  body is zstd-compressed on the client, so a small payload can expand to tens
  of GB; this cap rejects the upload once the decompressed stream exceeds the
  limit. Raise it for monorepos with large fixtures.
- `--basic-auth` — basic auth credentials (`username:password` format, env:
  `CI_BASIC_AUTH`)

### Storage Options

SQLite is the storage backend. See [Storage](../operations/storage.md) for full
details.

- `--storage-sqlite-path` — SQLite database file path (default: `pocketci.db`,
  env: `CI_STORAGE_SQLITE_PATH`)

### Secrets Options

SQLite is the default backend. Set `--secrets-s3-bucket` to use S3 instead. See
[Secrets](../operations/secrets.md) for full details.

- `--secrets-sqlite-path` — SQLite secrets database file (default:
  `pocketci.db`, env: `CI_SECRETS_SQLITE_PATH`)
- `--secrets-sqlite-passphrase` — encryption passphrase (env:
  `CI_SECRETS_SQLITE_PASSPHRASE`)
- `--secrets-s3-bucket` — S3 bucket name (env: `CI_SECRETS_S3_BUCKET`)
- `--secrets-s3-endpoint` — S3-compatible endpoint URL (env:
  `CI_SECRETS_S3_ENDPOINT`)
- `--secrets-s3-region` — AWS region (env: `CI_SECRETS_S3_REGION`)
- `--secrets-s3-access-key-id` — S3 access key ID (env:
  `CI_SECRETS_S3_ACCESS_KEY_ID`)
- `--secrets-s3-secret-access-key` — S3 secret access key (env:
  `CI_SECRETS_S3_SECRET_ACCESS_KEY`)
- `--secrets-s3-passphrase` — app-layer AES-256-GCM encryption key (env:
  `CI_SECRETS_S3_PASSPHRASE`)
- `--secrets-s3-encrypt` — provider SSE: `sse-s3`, `sse-kms`, or `sse-c` (env:
  `CI_SECRETS_S3_ENCRYPT`)
- `--secrets-s3-prefix` — S3 key prefix (env: `CI_SECRETS_S3_PREFIX`)

### Cache Options

Set `--cache-s3-bucket` to enable volume caching. See
[Caching](../operations/caching.md) for full details.

- `--cache-s3-bucket` — S3 bucket for cache storage (env: `CI_CACHE_S3_BUCKET`)
- `--cache-s3-endpoint` — S3-compatible endpoint URL (env:
  `CI_CACHE_S3_ENDPOINT`)
- `--cache-s3-region` — AWS region (env: `CI_CACHE_S3_REGION`)
- `--cache-s3-access-key-id` — S3 access key ID (env:
  `CI_CACHE_S3_ACCESS_KEY_ID`)
- `--cache-s3-secret-access-key` — S3 secret access key (env:
  `CI_CACHE_S3_SECRET_ACCESS_KEY`)
- `--cache-s3-prefix` — S3 key prefix (env: `CI_CACHE_S3_PREFIX`)
- `--cache-s3-ttl` — cache object TTL, e.g. `24h` (0 = no expiry, env:
  `CI_CACHE_S3_TTL`)
- `--cache-compression` — compression: `zstd`, `gzip`, or `none` (default:
  `zstd`, env: `CI_CACHE_COMPRESSION`)
- `--cache-key-prefix` — logical key prefix (env: `CI_CACHE_KEY_PREFIX`)

### OAuth Options

- `--oauth-github-client-id` — GitHub OAuth app client ID (env:
  `CI_OAUTH_GITHUB_CLIENT_ID`)
- `--oauth-github-client-secret` — GitHub OAuth app client secret (env:
  `CI_OAUTH_GITHUB_CLIENT_SECRET`)
- `--oauth-gitlab-client-id` — GitLab OAuth app client ID (env:
  `CI_OAUTH_GITLAB_CLIENT_ID`)
- `--oauth-gitlab-client-secret` — GitLab OAuth app client secret (env:
  `CI_OAUTH_GITLAB_CLIENT_SECRET`)
- `--oauth-gitlab-url` — GitLab instance URL for self-hosted (env:
  `CI_OAUTH_GITLAB_URL`)
- `--oauth-microsoft-client-id` — Microsoft OAuth app client ID (env:
  `CI_OAUTH_MICROSOFT_CLIENT_ID`)
- `--oauth-microsoft-client-secret` — Microsoft OAuth app client secret (env:
  `CI_OAUTH_MICROSOFT_CLIENT_SECRET`)
- `--oauth-microsoft-tenant` — Microsoft tenant ID (env:
  `CI_OAUTH_MICROSOFT_TENANT`)
- `--oauth-session-secret` — session/JWT signing secret (env:
  `CI_OAUTH_SESSION_SECRET`)
- `--oauth-callback-url` — public callback URL for OAuth redirects (env:
  `CI_OAUTH_CALLBACK_URL`)
- `--secure-cookies` — enable the `Secure` flag on session cookies; set this
  when the server is served over HTTPS so cookies are never sent over plain HTTP
  (env: `CI_SECURE_COOKIES`)
- `--server-rbac` — server-wide RBAC expression (env: `CI_SERVER_RBAC`)

> **Note:** Basic auth and OAuth are mutually exclusive. You cannot enable both
> at the same time.

See [Authentication](../operations/authentication.md) and
[Authorization](../operations/rbac.md) for full details.

## Example

```bash
pocketci server \
  --port 8080 \
  --storage-sqlite-path pocketci.db \
  --allowed-drivers docker \
  --basic-auth admin:secret123
```

With OAuth:

```bash
pocketci server \
  --port 8080 \
  --storage-sqlite-path pocketci.db \
  --allowed-drivers docker \
  --oauth-github-client-id YOUR_CLIENT_ID \
  --oauth-github-client-secret YOUR_CLIENT_SECRET \
  --oauth-session-secret "$(openssl rand -hex 32)" \
  --oauth-callback-url https://ci.example.com \
  --secure-cookies
```

> **HTTPS deployments:** Always pass `--secure-cookies` when serving over HTTPS.
> This prevents session cookies from being transmitted over plain HTTP
> connections.

The server provides:

- Web UI at `http://localhost:8080/pipelines/`
- JSON API at `http://localhost:8080/api/`
- Webhook endpoint at `http://localhost:8080/api/webhooks/:pipeline-id`
- MCP endpoint at `http://localhost:8080/mcp`

See [Server API](../api/index.md) for full endpoint documentation.
