# Getting Started

PocketCI is a local-first CI/CD runtime. You write pipelines in TypeScript, and
PocketCI runs them inside containers. No cloud account required — everything
runs on your machine.

This guide walks you from zero to a running pipeline in about five minutes.

## Prerequisites

- **Docker Desktop** installed and running
- **pocketci** installed:

```bash
brew tap jtarchie/pocketci https://github.com/jtarchie/pocketci
brew install pocketci
```

Or download a pre-built binary from the
[GitHub releases page](https://github.com/jtarchie/pocketci/releases).

Verify the install:

```bash
pocketci --version
```

## 1. Start the server

PocketCI runs as a local server that manages pipeline storage and execution.
Open a terminal and start it:

```bash
pocketci server --port 8080 --storage-sqlite-path pocketci.db
```

Leave this terminal running. The server stores pipeline definitions in
`pocketci.db` and serves the web UI at `http://localhost:8080/pipelines/`.

## 2. Write your first pipeline

::: code-group

```typescript [TypeScript (hello.ts)]
const pipeline = async () => {
  const result = await runtime.run({
    name: "hello",
    image: "busybox",
    command: { path: "echo", args: ["Hello from PocketCI!"] },
  });
  console.log(result.stdout);
};

export { pipeline };
```

```yaml [YAML (hello.yml)]
jobs:
  - name: hello
    plan:
      - task: hello
        config:
          platform: linux
          image_resource:
            type: registry-image
            source:
              repository: busybox
          run:
            path: echo
            args: ["Hello from PocketCI!"]
```

:::

The TypeScript pipeline is an async function exported as `pipeline`. Each
`runtime.run()` call launches a container:

- `name` — a label shown in logs and the UI
- `image` — the Docker image to use
- `command` — the executable and its arguments

The YAML format follows [Concourse CI](https://concourse-ci.org) syntax. See
[YAML Pipelines](./yaml-pipelines.md) for the full reference.

## 3. Register the pipeline

In a second terminal, register the pipeline with the server:

::: code-group

```bash [TypeScript]
pocketci pipeline set hello.ts \
  --server-url http://localhost:8080 \
  --name hello \
  --driver docker
```

```bash [YAML]
pocketci pipeline set hello.yml \
  --server-url http://localhost:8080 \
  --name hello \
  --driver docker
```

:::

The `--driver docker` flag tells the server to execute this pipeline using the
local Docker daemon. You can verify it was stored by visiting the web UI or
running:

```bash
pocketci pipeline ls --server-url http://localhost:8080
```

<a href="/screenshots/getting-started/02-pipeline-registered.png" target="_blank">
  <img src="/screenshots/getting-started/02-pipeline-registered.png" alt="Pipelines list showing the registered hello pipeline" />
</a>

## 4. Run the pipeline

Execute the pipeline and stream its output back to your terminal:

```bash
pocketci pipeline run hello --server-url http://localhost:8080
```

You should see:

```
Hello from PocketCI!
```

`pipeline run` waits for the pipeline to complete and exits with the same code
as the pipeline. This makes it easy to integrate with scripts and other tools.

<a href="/screenshots/getting-started/03-run-success.png" target="_blank">
  <img src="/screenshots/getting-started/03-run-success.png" alt="Pipeline detail page showing a successful run" />
</a>

## 5. Trigger the pipeline (async)

For fire-and-forget execution, use `trigger` instead:

```bash
pocketci pipeline trigger hello --server-url http://localhost:8080
```

This returns immediately with a run ID. Open the web UI to watch the run
progress at `http://localhost:8080/pipelines/hello`.

<a href="/screenshots/getting-started/04-triggered-run-completed.png" target="_blank">
  <img src="/screenshots/getting-started/04-triggered-run-completed.png" alt="Triggered run showing completed tasks" />
</a>

## Running without Docker

If you don't have Docker, use the `native` driver to run commands directly on
the host:

```bash
pocketci pipeline set hello.ts \
  --server-url http://localhost:8080 \
  --name hello \
  --driver native
```

With `native`, the `image` field is ignored and commands run in the current
environment.

## What's next

Ready to take it to production? The next guide walks through deploying PocketCI
to Fly.io, building a real multi-stage pipeline, and wiring up a GitHub webhook:

- [Production Setup](./production.md) — Fly.io deployment, multi-stage
  pipelines, and GitHub webhook integration

Or explore individual features:

- [Webhooks](./webhooks.md) — trigger pipelines from GitHub, GitLab, or any HTTP
  source
- [Scheduling](./scheduling.md) — run pipelines on a cron or interval schedule
- [YAML Pipelines](./yaml-pipelines.md) — use Concourse-compatible YAML instead
  of TypeScript
- [Secrets Management](../operations/secrets.md) — store and inject credentials
  securely
- [Runtime API](../runtime/) — full reference for `runtime.run()`, volumes, and
  more
