# Execution Queue

PocketCI limits concurrent pipeline runs with `--max-in-flight` (default 10).
When all slots are busy, incoming triggers can be queued rather than rejected.

## Configuration

| Flag               | Default | Env Var             | Description                                                |
| ------------------ | ------- | ------------------- | ---------------------------------------------------------- |
| `--max-in-flight`  | `10`    | `CI_MAX_IN_FLIGHT`  | Maximum concurrent pipeline executions                     |
| `--max-queue-size` | `100`   | `CI_MAX_QUEUE_SIZE` | Maximum queued runs waiting for a slot; 0 disables queuing |

### Example

```bash
pocketci server --max-in-flight 5 --max-queue-size 50
```

## How It Works

1. A pipeline trigger arrives (webhook, manual, schedule, or `triggerPipeline()`
   call).
2. If an in-flight slot is available, the run starts immediately.
3. If all slots are busy but the queue has capacity, the run is saved with
   status `queued`.
4. A background goroutine dispatches queued runs FIFO as slots free up.
5. If both in-flight slots and the queue are full, the server returns HTTP 503
   with an `ErrQueueFull` error.

## Run Status Lifecycle

Queued runs follow this status progression:

```
queued → running → succeeded / failed
```

Runs that start immediately skip the `queued` state.

## Queue-Full Behavior

When the queue is at capacity:

- **Webhook triggers** receive HTTP 503
- **Manual triggers** (`pipeline run`, `pipeline trigger`) receive HTTP 503
- **Resume operations** receive HTTP 503
- **Scheduled triggers** skip the current cycle and retry at the next poll
  interval
- **`triggerPipeline()` calls** throw a JavaScript error

## Disabling the Queue

Set `--max-queue-size 0` to disable queuing entirely. When disabled, any trigger
that arrives while all in-flight slots are busy is immediately rejected with
HTTP 503.

## Graceful Shutdown

On server shutdown:

1. New queued runs are no longer accepted.
2. The queue drains — already-queued runs are dispatched as slots free up.
3. The server waits for all in-flight runs to complete before exiting.

## Tuning Guidance

- **Webhook-heavy deployments**: Use a larger queue (e.g., 200-500) to absorb
  burst traffic and avoid dropping webhook events.
- **Interactive use**: A smaller queue with faster feedback may be preferable.
- **Scheduled pipelines**: The scheduler automatically respects queue limits and
  retries on the next poll cycle, so queue size is less critical.

## Per-Pipeline Concurrency Rules

The global `--max-in-flight` cap admits any pipeline into a slot. Concurrency
rules further restrict overlap **per pipeline** so two runs of "the same thing"
do not execute simultaneously.

Configure via `pocketci pipeline set` (see
[pipeline-set](../cli/pipeline-set.md)) or the `PUT /api/pipelines/:name`
endpoint:

| Field                        | Type    | Notes                                                      |
| ---------------------------- | ------- | ---------------------------------------------------------- |
| `concurrency_mode`           | string  | `""` (none), `"serial"`, `"group"`, or `"skip-if-running"` |
| `concurrency_group_template` | string  | Go `text/template`, required when `mode=group`             |
| `concurrency_cancel_running` | boolean | Only valid with `mode=group`; cancels in-flight peers      |

### Modes

- **`serial`** — one run per pipeline at a time. Additional triggers wait in
  `queued` status; the queue processor dispatches them in order as the in-flight
  peer finishes.
- **`group`** — runs that resolve to the same group key serialize against each
  other; different groups run in parallel.
  - `concurrency_cancel_running: false` (default): queue the new run behind its
    group peers, like serial-per-group.
  - `concurrency_cancel_running: true`: cancel any running peer in the same
    group and mark queued peers `skipped` with reason `"superseded by run X"`,
    then dispatch the new run. GitHub Actions-style "cancel in progress".
- **`skip-if-running`** — if any non-terminal run exists for the pipeline,
  record the new trigger as a `skipped` run with reason
  `"skipped: pipeline already running"`. Useful for noisy webhook sources.

`""` (empty) preserves the legacy behavior: no collision rules, runs overlap
freely up to `--max-in-flight`.

### Group templates

The template is rendered against the trigger input so the group can depend on
webhook fields:

```
deploy-{{.Webhook.Branch}}
preview-{{index .Webhook.Headers "X-Pull-Request"}}
{{.Provider}}-{{.Webhook.EventType}}
```

Available fields:

- `.Args` — `[]string` from `pocketci pipeline trigger --arg`
- `.Jobs` — `[]string` (job filter, when present)
- `.Webhook.Provider`, `.Webhook.EventType`, `.Webhook.Method`, `.Webhook.URL`
- `.Webhook.Branch`, `.Webhook.Ref` — convenience projections of the `X-Branch`
  / `X-Ref` headers
- `.Webhook.Headers`, `.Webhook.Query` — full header / query maps for
  provider-specific keys

A template that fails to parse or renders to an empty string records the trigger
as a `failed` run with the template error in `error_message`.

### Queue interaction

The queue processor is group-aware. If the oldest queued run's group is held by
a running peer, the processor skips it and tries the next queued run — runs in
**different** groups never block each other. When the in-flight peer finishes
and frees the group, the processor picks up the waiting run on the next signal.

### Observability

Events emitted to the log and metrics:

- `concurrency.skip` — `skip-if-running` recorded a new trigger as skipped
- `concurrency.supersede` — `group + cancel-in-progress` cancelled (running) or
  skipped (queued) a peer
- `queue.enqueued.group_busy` — a new run was queued because its group has an
  in-flight peer
- `pocketci_runs_skipped_total{reason="skip-if-running"|"superseded"}` —
  cumulative counter

### Examples

Serial deploys per pipeline:

```bash
pocketci pipeline set deploy.ts -s $URL --concurrency-mode serial
```

Per-branch deploy with auto-supersede (GitHub Actions style):

```bash
pocketci pipeline set deploy.ts -s $URL \
  --concurrency-mode group \
  --concurrency-group-template 'deploy-{{.Webhook.Branch}}' \
  --concurrency-cancel-running
```

Drop duplicate webhook events:

```bash
pocketci pipeline set ingest.ts -s $URL --concurrency-mode skip-if-running
```
