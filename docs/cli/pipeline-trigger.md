# pocketci pipeline trigger

Trigger async pipeline execution on a remote CI server. Unlike
`pocketci pipeline run` (which streams output synchronously), trigger fires the
pipeline and returns immediately with a run ID.

```bash
pocketci pipeline trigger <name> --server <url> [options]
```

## Options

- `--server`, `-s` — server URL (required; e.g., `http://localhost:8080`)
- `--auth-token`, `-t` — JWT auth token (env: `CI_AUTH_TOKEN`)
- `--config-file`, `-c` — auth config file path (env: `CI_AUTH_CONFIG`; default:
  `~/.pocketci/auth.config`)
- `--args`, `-a` — arguments passed to the pipeline (repeatable)
- `--webhook-body` — JSON body for simulated webhook trigger
- `--webhook-method` — HTTP method for simulated webhook (default: `POST`;
  options: `GET`, `POST`, `PUT`, `PATCH`, `DELETE`)
- `--webhook-header` — header for simulated webhook (repeatable; format:
  `KEY=VALUE`)

## Examples

### Manual trigger

```bash
pocketci pipeline trigger my-pipeline --server http://localhost:8080
```

### Trigger with arguments

```bash
pocketci pipeline trigger my-pipeline \
  --server http://localhost:8080 \
  -a "--env=staging" \
  -a "--verbose"
```

### Simulated webhook trigger

```bash
pocketci pipeline trigger my-pipeline \
  --server http://localhost:8080 \
  --webhook-body '{"action": "opened", "repository": {"full_name": "owner/repo"}}' \
  --webhook-method POST \
  --webhook-header "X-GitHub-Event=push" \
  --webhook-header "Content-Type=application/json"
```

## Authentication

With OAuth-enabled servers, authenticate first with `pocketci login`:

```bash
pocketci login -s https://ci.example.com
pocketci pipeline trigger my-pipeline -s https://ci.example.com
```

Or provide a token directly:

```bash
pocketci pipeline trigger my-pipeline \
  --server https://ci.example.com \
  --auth-token eyJhbGciOiJIUzI1NiIs...
```
