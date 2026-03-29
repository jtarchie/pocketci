# Scheduling Pipelines

PocketCI can run pipelines automatically on a schedule using cron expressions or
fixed intervals. Schedules are defined inline in YAML pipelines and managed via
the CLI or API.

## Prerequisites

The scheduling feature requires the `schedules` feature gate, which is **not**
included in the default `*` wildcard. Enable it explicitly:

```bash
pocketci server --allowed-features "*,schedules"
```

Or via environment variable:

```bash
export CI_ALLOWED_FEATURES="*,schedules"
pocketci server
```

See [Feature Gates](../operations/feature-gates.md) for more details.

## Defining Schedules in YAML

Add a `triggers.schedule` block to any job in your pipeline YAML:

```yaml
jobs:
  - name: nightly-build
    triggers:
      schedule:
        cron: "0 2 * * *"
    plan:
      - task: build
        config:
          image: golang:1.22
          run:
            path: go
            args: [build, ./...]

  - name: health-check
    triggers:
      schedule:
        every: "15m"
    plan:
      - task: check
        config:
          image: curlimages/curl
          run:
            path: curl
            args: [-f, "http://app:8080/health"]
```

Each job can have at most one schedule trigger. Use either `cron` or `every`,
not both.

## Cron Expressions

Standard 5-field cron format: `minute hour day-of-month month day-of-week`.

| Expression     | Description             |
| -------------- | ----------------------- |
| `0 2 * * *`    | Daily at 2:00 AM        |
| `*/15 * * * *` | Every 15 minutes        |
| `0 9 * * 1-5`  | Weekdays at 9:00 AM     |
| `0 0 1 * *`    | First day of each month |
| `30 6 * * 0`   | Sundays at 6:30 AM      |

## Interval Expressions

Go duration strings specify a fixed interval between runs:

| Expression | Description      |
| ---------- | ---------------- |
| `5m`       | Every 5 minutes  |
| `1h`       | Every hour       |
| `12h`      | Every 12 hours   |
| `1h30m`    | Every 90 minutes |

The next run is computed as `last_run + interval`. If a schedule has never run,
the first execution happens one interval after the pipeline is set.

## How the Scheduler Works

1. A background goroutine polls the database every 10 seconds for due schedules.
2. Due schedules are claimed atomically using `UPDATE ... RETURNING`, preventing
   double-fire in multi-instance deployments.
3. After claiming, the scheduler triggers the pipeline and computes the next run
   time.
4. If the [execution queue](../operations/execution-queue.md) is full, the
   trigger is skipped and retried at the next poll interval.

## Job Targeting

When a schedule is attached to a specific job, only that job (and its downstream
dependents) runs. This is visible in the run metadata:

- `trigger_type` is set to `schedule`
- `triggered_by` shows `schedule:<schedule-name>`

## Managing Schedules

### CLI

List all schedules for a pipeline:

```bash
pocketci pipeline schedule ls my-pipeline --server http://localhost:8080
```

Pause a schedule to temporarily stop it from firing:

```bash
pocketci pipeline schedule pause my-pipeline nightly-build \
  --server http://localhost:8080
```

Unpause to resume:

```bash
pocketci pipeline schedule unpause my-pipeline nightly-build \
  --server http://localhost:8080
```

See [Pipeline Schedule CLI](../cli/schedule.md) for full reference.

### API

- `GET /api/pipelines/:id/schedules` — list all schedules
- `PUT /api/schedules/:id/enabled` — pause or unpause

See [Schedules API](../api/schedules.md) for full reference.

## Triggering Pipelines from JavaScript

The `triggerPipeline()` global function allows a running pipeline to
programmatically trigger another pipeline:

```typescript
// Trigger another pipeline by name
const runId = await triggerPipeline("deploy-staging");

// Target specific jobs
const runId = await triggerPipeline("integration-tests", {
  jobs: ["api-tests", "ui-tests"],
});

// Pass arguments
const runId = await triggerPipeline("release", {
  args: ["--version=2.0.0"],
});
```

### Signature

```typescript
triggerPipeline(
  pipelineName: string,
  options?: {
    jobs?: string[];
    args?: string[];
  }
): Promise<string>
```

Returns a Promise that resolves with the triggered run's ID.

## Interaction with the Execution Queue

Scheduled triggers respect the server's concurrency limits. If both in-flight
slots and the queue are full, the scheduled trigger is skipped for that poll
cycle and retried on the next interval. Configure queue capacity with
`--max-queue-size` (see [Execution Queue](../operations/execution-queue.md)).
