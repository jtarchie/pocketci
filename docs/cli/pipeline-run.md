# pocketci pipeline run

Execute a stored pipeline by name on a remote CI server.

```bash
pocketci pipeline run <name> [args...] --server-url <url> [options]
```

## Options

- `--server-url` — CI server URL (required; env: `CI_SERVER_URL`)
- `--timeout` — client-side deadline (env: `CI_TIMEOUT`)
- `--no-workdir` — do not mount the current directory as a volume
- `--ignore` — glob patterns to exclude from workdir mount (repeatable)
- `--auth-token` — JWT auth token (env: `CI_AUTH_TOKEN`)
- `--config-file` — auth config file path (env: `CI_AUTH_CONFIG`; default:
  `~/.pocketci/auth.config`)

All positional arguments after `<name>` are passed to the pipeline as
`pipelineContext.args`.

## Example

```bash
pocketci pipeline run my-pipeline arg1 arg2 --server-url http://localhost:8080
```

Inside the pipeline, `pipelineContext.args === ["arg1", "arg2"]`.

## Authentication

With OAuth-enabled servers, authenticate first with `pocketci login`:

```bash
pocketci login -s https://ci.example.com
pocketci pipeline run my-pipeline --server-url https://ci.example.com
```

Or provide a token directly:

```bash
pocketci pipeline run my-pipeline \
  --server-url https://ci.example.com \
  --auth-token eyJhbGciOiJIUzI1NiIs...
```

See [Run Pipelines](../guides/run.md) for detailed examples including k6 load
testing and background execution.
