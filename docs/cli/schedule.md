# pocketci pipeline schedule

Manage pipeline schedules: list, pause, and unpause scheduled triggers.

Requires the `schedules` [feature gate](../operations/feature-gates.md) to be
enabled on the server.

## schedule ls

List all schedules for a pipeline.

```bash
pocketci pipeline schedule ls <pipeline> --server <url> [options]
```

### Example

```bash
pocketci pipeline schedule ls my-pipeline --server http://localhost:8080
```

Output shows each schedule's name, type, expression, status, and next run time.

## schedule pause

Pause a schedule to stop it from firing.

```bash
pocketci pipeline schedule pause <pipeline> <schedule-name> --server <url> [options]
```

### Example

```bash
pocketci pipeline schedule pause my-pipeline nightly-build \
  --server http://localhost:8080
```

## schedule unpause

Unpause a schedule to resume automatic triggering.

```bash
pocketci pipeline schedule unpause <pipeline> <schedule-name> --server <url> [options]
```

### Example

```bash
pocketci pipeline schedule unpause my-pipeline nightly-build \
  --server http://localhost:8080
```

## Options

All subcommands accept:

- `--server` — server URL (required; e.g., `http://localhost:8080`)
- `--auth-token` — JWT auth token (env: `CI_AUTH_TOKEN`)
- `--config-file` — auth config file path (env: `CI_AUTH_CONFIG`; default:
  `~/.pocketci/auth.config`)

## Authentication

With OAuth-enabled servers, authenticate first with `pocketci login`:

```bash
pocketci login -s https://ci.example.com
pocketci pipeline schedule ls my-pipeline -s https://ci.example.com
```

Or provide a token directly:

```bash
pocketci pipeline schedule ls my-pipeline \
  --server https://ci.example.com \
  --auth-token eyJhbGciOiJIUzI1NiIs...
```
