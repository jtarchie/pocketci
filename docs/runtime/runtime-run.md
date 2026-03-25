# runtime.run()

Execute a container task.

```typescript
const result = await runtime.run(options);
```

## Options

- `name` — task name (string)
- `image` — container image (e.g., `"alpine:latest"`, required)
- `command` — command to run (required)
  - `path` — executable or script path
  - `args` — command arguments (array)
- `env` (optional) — environment variables object (supports `secret:KEY` prefix)
- `mounts` (optional) — volume mounts: `{ "/container/path": volumeHandle }`
- `caches` (optional) — cache paths (for S3-backed caching)
- `inputVariables` (optional) — named inputs for resource operations

## Return Value

```typescript
{
  code: number; // exit code
  stdout: string; // captured stdout (redacted if secrets used)
  stderr: string; // captured stderr (redacted)
  startedAt: string; // ISO timestamp
  endedAt: string; // ISO timestamp
}
```

## Example

```typescript
const result = await runtime.run({
  name: "test",
  image: "golang:1.22",
  command: { path: "go", args: ["test", "./..."] },
  env: {
    GOFLAGS: "-race",
    DB_PASSWORD: "secret:db_password", // resolved at runtime
  },
});

if (result.code !== 0) {
  throw new Error(`tests failed: ${result.stderr}`);
}
```

See [Secrets](../operations/secrets.md) for secret injection details.

## YAML External Task Config

In Concourse-compatible YAML, a task step can load its config from an external
source instead of inlining it:

- **`file`** — load from a volume mount (path format: `mountname/relative/path`)
- **`uri`** — load from a URI (`file://`, `http://`, `https://`)

`file` and `uri` are mutually exclusive. See
[Loading config from a URI](runtime-agent.md#uri) for full details and examples.

```yaml
# Load from a volume
- task: build
  file: repo/tasks/build.yml

# Load from a remote URL
- task: build
  uri: "https://example.com/tasks/build.yml"
```

## YAML Parallelism And Throttling

When using Concourse-compatible YAML, task fan-out and throttling are available
at the step, job, and pipeline levels:

- `parallelism` on a `task` step expands that step into N parallel task
  instances.
- `max_in_flight` on `job` limits concurrent work inside that job.
- `max_in_flight` at pipeline root provides a fallback limit for jobs that do
  not set their own `max_in_flight`.
- `in_parallel.limit` limits concurrent substeps for an `in_parallel` block.
- `across.max_in_flight` limits concurrent `across` combinations.

Precedence for `in_parallel` concurrency (highest to lowest):

1. `job.max_in_flight` (or `pipeline.max_in_flight` if no job-level value)
2. `in_parallel.limit`
3. Unlimited (all substeps run concurrently)

This means a job-level `max_in_flight` cap applies even if `in_parallel.limit`
is set higher.

Parallel task instances receive these environment variables:

- `CI_TASK_COUNT`: total number of instances in the fan-out set.
- `CI_TASK_INDEX`: 1-based index of the current instance.

Example:

```yaml
max_in_flight: 4

jobs:
  - name: test
    max_in_flight: 2
    plan:
      - task: unit
        parallelism: 3
        config:
          platform: linux
          image_resource:
            type: registry-image
            source:
              repository: busybox
          run:
            path: sh
            args: ["-c", "echo $CI_TASK_INDEX/$CI_TASK_COUNT"]
```

## Current Limitation

Sharing the same volume handle across parallel instances of the same task is
currently undefined behavior. Different orchestration drivers may behave
differently under concurrent reads/writes to the same mounted volume.

For now, avoid relying on concurrent shared-volume mutation within a single
parallelized task set.
