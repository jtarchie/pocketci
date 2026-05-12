# YAML Pipelines

PocketCI supports two pipeline formats: **JavaScript/TypeScript** (the primary
format) and **Concourse-compatible YAML**. YAML pipelines are a good fit if
you're migrating from [Concourse CI](https://concourse-ci.org) or prefer a
declarative style for resource-driven workflows.

## Pipeline Structure

A YAML pipeline has three top-level sections:

```yaml
resource_types: [] # optional — custom resource type definitions
resources: [] # resources to get/put during jobs
jobs: [] # the work to execute
```

### Resources

Resources represent external artifacts (git repos, S3 buckets, container images,
etc.) that jobs fetch or publish:

```yaml
resource_types:
  - name: mock
    type: registry-image
    source:
      repository: concourse/mock-resource

resources:
  - name: source-code
    type: mock
    source:
      force_version: "1.0"
```

The built-in resource type is `registry-image`. Any additional type must be
declared under `resource_types`.

### Jobs

Each job has a `plan` — an ordered list of steps to execute:

```yaml
jobs:
  - name: build
    plan:
      - get: source-code
      - task: compile
        config:
          platform: linux
          image_resource:
            type: registry-image
            source:
              repository: golang:1.22
          run:
            path: go
            args: ["build", "./..."]
```

## Step Types

| Step          | Description                        |
| ------------- | ---------------------------------- |
| `task`        | Run a command in a container       |
| `get`         | Fetch a resource version           |
| `put`         | Publish to a resource              |
| `do`          | Group steps sequentially           |
| `try`         | Run steps, absorbing failures      |
| `in_parallel` | Run steps concurrently             |
| `across`      | Fan-out steps over a set of values |
| `attempts`    | Retry a step on failure            |
| `notify`      | Send a notification                |
| `agent`       | Run an LLM agent step              |

### task

The most common step. Runs a command inside a container:

```yaml
- task: run-tests
  config:
    platform: linux
    image_resource:
      type: registry-image
      source:
        repository: busybox
    run:
      path: sh
      args: ["-c", "echo hello"]
  timeout: 10m
```

### Task Configuration Reference

#### `run` — working directory

Use `run.dir` to set the working directory inside the container. Relative paths
are resolved under `/workspace`; absolute paths are used as-is.

```yaml
- task: test
  config:
    platform: linux
    image_resource:
      type: registry-image
      source:
        repository: golang
    inputs:
      - name: repo
    run:
      dir: repo # → /workspace/repo
      path: go
      args: [test, ./...]
```

#### `env` — environment variables and secrets

Pass environment variables to the task. Values prefixed with `secret:` are
resolved from the pipeline's secrets store at runtime:

```yaml
- task: deploy
  config:
    ...
    env:
      DEPLOY_ENV: production
      API_TOKEN: "secret:DEPLOY_API_TOKEN"  # resolved from secrets
```

See [Secrets](../operations/secrets.md) for how to register secrets.

#### `limits` — container resource limits

Control CPU and memory allocation for the container. Supported by the Fly.io
driver; other drivers may ignore these fields.

| Field      | Type   | Description                                     |
| ---------- | ------ | ----------------------------------------------- |
| `cpu`      | int    | Number of vCPUs                                 |
| `memory`   | string | Memory with unit suffix: `512MB`, `4GB`, `2GiB` |
| `cpu_kind` | string | CPU class: `shared` (default) or `performance`  |

```yaml
- task: heavy-build
  config:
    platform: linux
    limits:
      cpu_kind: performance # dedicated CPUs (Fly.io)
      cpu: 4
      memory: 8GB
    image_resource:
      type: registry-image
      source:
        repository: golang
    run:
      path: go
      args: [build, ./...]
```

`performance` CPUs provide dedicated (non-shared) compute. Memory for
performance machines is rounded up to the nearest 1 GB; shared machines round to
the nearest 256 MB.

### notify

Send a notification to a configured backend (Slack, Teams, HTTP webhook). The
`notify` key is the name (or list of names) of a notification config registered
with the pipeline.

```yaml
# Inline message with Go/Sprig template
- notify: my-webhook
  message: "Build {{ .JobName }} finished: {{ .Status | upper }}"

# Multiple destinations
- notify:
    - slack-channel
    - teams-webhook
  message: "Deploy done"

# Fire-and-forget (does not block or fail the step on error)
- notify: audit-log
  message: "Pipeline {{ .PipelineName }} started"
  async: true

# Load the message template from a file in a prior task's output volume
- notify: my-webhook
  message_file: task-output/message.txt
```

The `message` and `message_file` fields are rendered as Go
[`text/template`](https://pkg.go.dev/text/template) strings with
[Sprig](https://masterminds.github.io/sprig/) functions. The template context
exposes `.PipelineName`, `.JobName`, `.BuildID`, `.Status`, `.StartTime`,
`.EndTime`, `.Duration`, `.Environment`, and `.TaskResults`.

When `message_file` is set it takes precedence over `message`. The path format
is `<volume-name>/<relative-path>`, matching the same convention used by `task`
`file:` and `agent` `prompt_file:` fields.

### get / put

Fetch and publish resources:

```yaml
- get: source-code
  passed: [build] # only trigger after "build" job succeeds

- put: artifact-store
  params:
    file: output/binary
```

### in_parallel

Run steps concurrently with optional concurrency limit:

```yaml
- in_parallel:
    limit: 2
    fail_fast: true
    steps:
      - task: lint
        config: ...
      - task: unit-tests
        config: ...
      - task: integration-tests
        config: ...
```

### do / try

Group steps or absorb failures:

```yaml
- do:
    - task: step-a
      config: ...
    - task: step-b
      config: ...
  on_success:
    task: notify-success
    config: ...

- try:
    - task: optional-step
      config: ...
```

## Job Dependencies

Use `passed` constraints on `get` steps to define a dependency between jobs. The
dependent job only runs after the specified job has successfully used the same
resource:

```yaml
jobs:
  - name: build
    plan:
      - get: source-code
      - task: compile
        config: ...

  - name: deploy
    plan:
      - get: source-code
        passed: [build] # waits for "build" to succeed
      - task: deploy-app
        config: ...
```

> `passed:` is only valid on `get` steps. Putting it on a `task`, `build_image`,
> or `put` step is rejected at upsert with an error redirecting you to
> `triggers.passed` (see [Per-Job Triggers](#per-job-triggers) below).

## Per-Job Triggers

Each job's `triggers:` block declares which events fire it: webhooks, schedules,
or fan-in completion of other jobs. **A job with no `triggers:` block keeps the
legacy behavior** — it runs on any trigger including manual — so existing
pipelines need no changes.

| `triggers:` on job                      | Webhook | Schedule | Manual | `triggers.passed` completion |
| --------------------------------------- | ------- | -------- | ------ | ---------------------------- |
| absent (legacy)                         | runs    | runs     | runs   | runs if dependent            |
| only `triggers.webhook`                 | runs    | skip     | skip   | skip                         |
| only `triggers.schedule`                | skip    | runs     | skip   | skip                         |
| only `triggers.passed`                  | skip    | skip     | skip   | runs                         |
| webhook + schedule (or any combination) | runs    | runs     | skip   | runs only if also in passed  |

Multiple trigger types compose with **OR**: a job declaring both
`triggers.webhook` and `triggers.passed` fires on either.

Manual `pocketci pipeline trigger <name>` without `--job` fires only jobs that
declare _no_ `triggers:` block (strict opt-in for trigger-declared jobs). Use
`pocketci pipeline trigger <name> --job <job>` to force-run any job, bypassing
the filter.

### `triggers.schedule` — cron and intervals

```yaml
jobs:
  - name: nightly-build
    triggers:
      schedule:
        cron: "0 2 * * *" # exactly one of cron or every
    # every: "24h"
    plan:
      - task: build
        config: ...
```

### `triggers.webhook` — filter expressions

```yaml
jobs:
  - name: run-tests
    triggers:
      webhook:
        filter: 'payload.ref == "refs/heads/main"' # optional expr-lang filter
        dedup_key: "payload.id" # optional dedup hash key
    plan:
      - task: tests
        config: ...
```

Dedup is **per-job**, not pipeline-wide: with multiple jobs declaring the same
`dedup_key`, a duplicate webhook can run a subset of jobs.

### `triggers.passed` — DAG fan-in

A job fires when **all** named upstream jobs have a successful run **since this
job's last run** (of any status). Failed upstreams don't propagate, and a failed
downstream doesn't block future re-firings — the freshness clock advances on
every run.

```yaml
jobs:
  - name: a
    triggers: { schedule: { cron: "0 1 * * *" } }
    plan: [...]

  - name: d
    triggers: { webhook: {} }
    plan: [...]

  - name: b
    triggers:
      passed: [a, d] # fires when both a AND d succeed since b's last run
    plan: [...]

  - name: c
    triggers:
      passed: [b]
    plan: [...]
```

The completion scanner runs after every successful job and is **coalescing** —
if a downstream run is already queued or running, additional upstream successes
do not queue duplicates. A boot-time recovery sweep handles the case where the
server crashed between an upstream's success and the scanner.

**Bootstrap a new pipeline**: the first time you ship a chain like A → B with
`triggers.passed: [a]` on B, B has nothing to fire from. Either let A run on its
own trigger first, or use
[`pocketci pipeline seed-passed`](../cli/pipeline-set.md) to record a synthetic
success and unblock B.

**Validation at upsert**: cycles across `triggers.passed` edges, unknown
upstream names, self-reference, empty `passed:` lists, and pipelines with no
leaf trigger (every job is `triggers.passed`-only) are rejected with explicit
errors.

### Worked example: split build + test

Pre-feature, a single pipeline rebuilds the CI base image on every push then
runs tests:

```yaml
jobs:
  - name: build-and-test # rebuilds the image every push (slow!)
    plan:
      - task: build-image
      - task: run-tests
```

After splitting with per-job triggers, the image rebuilds on a schedule while
tests run on every webhook:

```yaml
jobs:
  - name: build-image
    triggers:
      schedule:
        cron: "0 2 * * 0" # Sunday 02:00
    plan:
      - task: build
        config: ...

  - name: run-tests
    triggers:
      webhook: {}
    plan:
      - task: tests
        config:
          image_resource:
            type: registry-image
            source:
              repository: registry.example.com/ci-base
              tag: latest
```

### Per-job concurrency

To prevent two scheduled `build-image` runs from racing without blocking
unrelated test runs, set the pipeline's
[concurrency mode](../operations/execution-queue.md#per-pipeline-concurrency-rules)
to `group` with a job-keyed template:

```bash
pocketci pipeline set ci.yml -s $URL \
  --concurrency-mode group \
  --concurrency-group-template '{{ if .Jobs }}{{ index .Jobs 0 }}{{ else }}all{{ end }}'
```

Each targeted job becomes its own concurrency group: `build-image` queues behind
`build-image`, `run-tests` queues behind `run-tests`, and different jobs run in
parallel.

## Step and Job Hooks

Hooks run conditionally based on outcome. They can be attached to any step or to
the job itself:

| Hook         | Triggers when                                  |
| ------------ | ---------------------------------------------- |
| `on_success` | Step/job succeeded                             |
| `on_failure` | Step/job failed (non-zero exit)                |
| `on_error`   | Step/job errored (infrastructure issue)        |
| `on_abort`   | Step/job was aborted (timeout or cancellation) |
| `ensure`     | Always — runs regardless of outcome            |

```yaml
jobs:
  - name: deploy
    plan:
      - task: run-migration
        config: ...
        on_failure:
          task: rollback
          config: ...
    on_success:
      task: notify-success
      config: ...
    ensure:
      task: cleanup
      config: ...
```

## Concurrency Control

Limit how many runs of a job can be active simultaneously:

```yaml
jobs:
  - name: deploy
    max_in_flight: 1 # only one deploy at a time
    plan:
      - task: deploy
        config: ...
```

## Template Preprocessing

YAML pipelines support Go `text/template` syntax for dynamic content. Opt in by
adding a comment at the top of the file:

```yaml
# pocketci: template
jobs:
  - name: {{ .Env.BUILD_ENV }}-deploy
    plan:
      - task: deploy
        config: ...
```

See the [Templating guide](../runtime/templating.md) for full details.

## Known Limitations

PocketCI's YAML support is intentionally scoped. The following are not
supported:

- Overlay/btrfs volume management (container runtimes handle volumes natively)
- Tasks spread across multiple workers within a single job
- Full Concourse feature parity (this is a compatibility layer, not a
  reimplementation)
