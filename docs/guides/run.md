# pocketci run

Execute a stored pipeline by name on a remote CI server. The server runs the
pipeline; the client is a thin HTTP layer that streams output back to your
terminal in real time.

**Nothing runs locally.** Drivers, secrets, and configuration all live on the
server. `pocketci run` is just a phone call to `pocketci server`.

## Quick start

Register a pipeline once:

```bash
pocketci set-pipeline k6.ts \
  --name k6 \
  --server-url http://localhost:8080 \
  --driver docker
```

Run it — everything after the pipeline name is forwarded to the pipeline as
`pipelineContext.args`:

```bash
pocketci run k6 run --vus=10 --duration=30s script.js \
  --server-url http://localhost:8080
```

## CLI syntax

```
pocketci run <name> [args...] --server-url <url> [--timeout <duration>]
```

| Flag / Env var                   | Default      | Description                              |
| -------------------------------- | ------------ | ---------------------------------------- |
| `--server-url` / `CI_SERVER_URL` | _(required)_ | URL of the `pocketci server` instance    |
| `--timeout` / `CI_TIMEOUT`       | none         | Client-side deadline for the full stream |

All positional arguments after `<name>` are collected verbatim and passed
through to the pipeline (including flags such as `--vus=10`).

## How it works

1. The client POSTs `{"args": [...]}` to `POST /api/pipelines/<name>/run`.
2. The server looks up the pipeline by name, creates a run record, and executes
   the pipeline synchronously using the driver stored with it.
3. The response is an SSE stream. Each event is a JSON object:
   - `{"stream":"stdout","data":"..."}` — container stdout chunk
   - `{"stream":"stderr","data":"..."}` — container stderr chunk
   - `{"event":"exit","code":0,"run_id":"..."}` — pipeline finished
   - `{"event":"error","message":"..."}` — pipeline not found or fatal error
4. The client writes stdout/stderr to the terminal and exits with the pipeline's
   exit code.

## Pipeline API

Inside the pipeline, `pipelineContext.args` is a string array containing
everything the caller passed after the pipeline name:

```typescript
// pipelineContext.args === ["run", "--vus=10", "script.js"]
const result = await runtime.run({
  name: "my-task",
  image: "my-image",
  command: { path: "my-tool", args: pipelineContext.args },
});
```

## k6 load-testing example

See [examples/run/k6.ts](../../examples/run/k6.ts) for a complete example.

```typescript
const pipeline = async () => {
  const workdir = await runtime.createVolume("workdir", 200);

  const result = await runtime.run({
    name: "k6",
    image: "grafana/k6:latest",
    command: {
      path: "k6",
      args: pipelineContext.args,
    },
    mounts: {
      "/workspace": workdir,
    },
  });

  if (result.code !== 0) {
    throw new Error(`k6 exited with code ${result.code}:\n${result.stderr}`);
  }
};

export { pipeline };
```

Register it and run your load test from the directory containing `script.js`:

```bash
# Register once
pocketci set-pipeline k6.ts --name k6 --server-url http://localhost:8080 --driver docker

# Run from any directory — the current directory is available at /workspace
pocketci run k6 run --vus=10 --duration=30s /workspace/script.js \
  --server-url http://localhost:8080
```

The driver is a server concern — the same `pocketci run` command works whether
the server uses Docker, Kubernetes, Hetzner, or any other configured driver.

## Server configuration

No extra server flags are required beyond the standard `pocketci server` setup.
The `/api/pipelines/:name/run` endpoint is always available.

```bash
pocketci server \
  --port 8080 \
  --storage-sqlite-path pocketci.db \
  --allowed-drivers docker
```

## Secrets

Secrets are resolved server-side using the credentials stored with
`pocketci server`. The client has no access to secrets — pass them via
`pocketci set-pipeline` or the server's secrets backend, not as arguments to
`pocketci run`.

See [secrets](../operations/secrets.md) for details.
