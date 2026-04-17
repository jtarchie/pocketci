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

## Limits

The client compresses your working directory (excluding `--ignore` patterns)
with zstd and uploads it as the `workdir` part of the request. The server caps
the **decompressed** size to protect against zstd-bomb uploads.

- **Default cap:** 1 GiB decompressed. Sufficient for typical workdirs after
  `.gitignore`-style filtering on the client.
- **Override:** raise the server's `--max-workdir-mb` flag (env
  `CI_MAX_WORKDIR_MB`) on monorepos with large checked-in fixtures.
- **Exceeding the cap** surfaces as an SSE `error` event with
  `workdir decompressed size exceeds cap` before the pipeline starts.

See [pocketci server](server.md#options) for the server-side flag.
