# pocketci pipeline unpause

Unpause a pipeline to allow new runs.

```bash
pocketci pipeline unpause <name> --server <url> [options]
```

## Options

- `--server` — server URL (required; e.g., `http://localhost:8080`)
- `--auth-token` — JWT auth token (env: `CI_AUTH_TOKEN`)
- `--config-file` — auth config file path (env: `CI_AUTH_CONFIG`; default:
  `~/.pocketci/auth.config`)

## Example

```bash
pocketci pipeline unpause my-pipeline --server http://localhost:8080
```

## Authentication

With OAuth-enabled servers, authenticate first with `pocketci login`:

```bash
pocketci login -s https://ci.example.com
pocketci pipeline unpause my-pipeline -s https://ci.example.com
```

Or provide a token directly:

```bash
pocketci pipeline unpause my-pipeline \
  --server https://ci.example.com \
  --auth-token eyJhbGciOiJIUzI1NiIs...
```
