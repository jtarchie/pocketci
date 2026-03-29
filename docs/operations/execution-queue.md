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
