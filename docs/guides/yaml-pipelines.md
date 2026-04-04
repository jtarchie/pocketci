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
