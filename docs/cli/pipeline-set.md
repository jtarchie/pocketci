# pocketci pipeline set

Store a pipeline on a remote CI server.

```bash
pocketci pipeline set <pipeline-file> --server <url> [options]
```

## Options

- `--server` — server URL (required; e.g., `http://localhost:8080`)
- `--name` — pipeline name (if omitted, derived from filename)
- `--driver` — orchestration driver (e.g., `docker`, `native`, `k8s`)
- `--docker-host` — Docker daemon host URL (env: `CI_DOCKER_HOST`)
- `--fly-token`, `--fly-app`, `--fly-region`, `--fly-org`, `--fly-size`,
  `--fly-disk-gb` — Fly.io driver config (`--fly-disk-gb` sets the workspace
  volume size in GB; default 10)
- `--hetzner-token`, `--hetzner-image`, `--hetzner-server-type`,
  `--hetzner-location` — Hetzner driver config
- `--digitalocean-token`, `--digitalocean-image`, `--digitalocean-size`,
  `--digitalocean-region` — DigitalOcean driver config
- `--k8s-kubeconfig`, `--k8s-namespace` — Kubernetes driver config
- `--qemu-memory`, `--qemu-cpus`, `--qemu-accel`, `--qemu-image` — QEMU driver
  config
- `--webhook-secret` — secret for webhook requests (optional)
- `--basic-auth-username` — server basic auth user (env:
  `CI_BASIC_AUTH_USERNAME`)
- `--basic-auth-password` — server basic auth password (env:
  `CI_BASIC_AUTH_PASSWORD`)
- `--secret` — set pipeline secret (repeatable; format: `KEY=VALUE`)
- `--secret-file` — load secrets from a file (repeatable; format:
  `KEY=filepath`)
- `--resume` — enable resume support for the pipeline
- `--rbac` — RBAC expression restricting pipeline access (env: `CI_RBAC`)
- `--concurrency-mode` — collision rule applied at trigger time; one of `""`
  (default), `serial`, `group`, or `skip-if-running` (env:
  `CI_CONCURRENCY_MODE`). See
  [Per-Pipeline Concurrency Rules](../operations/execution-queue.md#per-pipeline-concurrency-rules).
- `--concurrency-group-template` — Go `text/template` rendered against the
  trigger input; required when `--concurrency-mode=group` (env:
  `CI_CONCURRENCY_GROUP_TEMPLATE`)
- `--concurrency-cancel-running` — when `--concurrency-mode=group`, cancel any
  in-flight peer in the same group before dispatching the new run (env:
  `CI_CONCURRENCY_CANCEL_RUNNING`)
- `--auth-token` — JWT auth token (env: `CI_AUTH_TOKEN`)
- `--config-file` — auth config file path (env: `CI_AUTH_CONFIG`; default:
  `~/.pocketci/auth.config`)

## Example

```bash
pocketci pipeline set my-pipeline.ts \
  --server http://localhost:8080 \
  --name my-pipeline \
  --driver docker \
  --webhook-secret my-secret-key
```

Once stored, trigger with `pocketci pipeline run`:

```bash
pocketci pipeline run my-pipeline --server-url http://localhost:8080
```

## Authentication

With OAuth-enabled servers, authenticate first with `pocketci login`:

```bash
pocketci login -s https://ci.example.com
pocketci pipeline set my-pipeline.ts -s https://ci.example.com
```

Or provide a token directly:

```bash
pocketci pipeline set my-pipeline.ts \
  --server https://ci.example.com \
  --auth-token eyJhbGciOiJIUzI1NiIs...
```

## RBAC

Restrict who can access a pipeline:

```bash
pocketci pipeline set my-pipeline.ts \
  --server https://ci.example.com \
  --rbac '"deploy-team" in Organizations'
```

See [Authorization](../operations/rbac.md) for expression syntax.

## Concurrency

Limit how multiple triggers of this pipeline overlap:

```bash
# Serial: one run at a time, queue the rest.
pocketci pipeline set deploy.ts -s $URL --concurrency-mode serial

# Group with cancel-in-progress: newer trigger supersedes the older.
pocketci pipeline set deploy.ts -s $URL \
  --concurrency-mode group \
  --concurrency-group-template 'deploy-{{.Webhook.Branch}}' \
  --concurrency-cancel-running

# Drop duplicate triggers while one is still in flight.
pocketci pipeline set ingest.ts -s $URL --concurrency-mode skip-if-running
```

See
[Per-Pipeline Concurrency Rules](../operations/execution-queue.md#per-pipeline-concurrency-rules)
for the full semantics, group-template reference, and observability events.
