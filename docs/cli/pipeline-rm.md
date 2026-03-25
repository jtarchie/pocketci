# pocketci pipeline rm

Remove a pipeline from a remote CI server.

```bash
pocketci pipeline rm <name> --server <url> [options]
```

## Options

- `--server` — server URL (required; e.g., `http://localhost:8080`)
- `--basic-auth-username` — server basic auth user (env:
  `CI_BASIC_AUTH_USERNAME`)
- `--basic-auth-password` — server basic auth password (env:
  `CI_BASIC_AUTH_PASSWORD`)
- `--auth-token` — JWT auth token (env: `CI_AUTH_TOKEN`)
- `--config-file` — auth config file path (env: `CI_AUTH_CONFIG`; default:
  `~/.pocketci/auth.config`)

## Example

```bash
pocketci pipeline rm my-pipeline \
  --server http://localhost:8080
```

## Authentication

With OAuth-enabled servers, authenticate first with `pocketci login`:

```bash
pocketci login -s https://ci.example.com
pocketci pipeline rm old-pipeline -s https://ci.example.com
```

Or provide a token directly:

```bash
pocketci pipeline rm old-pipeline \
  --server https://ci.example.com \
  --auth-token eyJhbGciOiJIUzI1NiIs...
```
