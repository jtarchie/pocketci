# Runs API

Query pipeline execution history and logs.

## List Runs

`GET /api/runs`

Get paginated execution history.

```bash
curl http://localhost:8080/api/runs
```

## Get Run Status

`GET /api/runs/:run_id/status`

Retrieve a specific run's metadata and status.

```bash
curl http://localhost:8080/api/runs/run-id-123/status
```

Response includes:

- `id` — run ID
- `pipeline_id` — owning pipeline
- `status` — `queued`, `running`, `success`, `failed`, `skipped`
- `trigger_type` — what initiated the run: `manual`, `webhook`, `cli`, `resume`
- `triggered_by` — who/what triggered it (e.g. `github:push`, `github:jdoe`)
- `trigger_input` — full input data for retrigger support:
  - `args` — CLI or manual arguments (if any)
  - `webhook` — full webhook request (`provider`, `eventType`, `method`, `headers`, `body`, `query`)
- `started_at`, `completed_at`, `created_at` — timestamps
- `error_message` — optional failure reason

## Get Run Tasks

`GET /api/runs/:run_id/tasks`

List all tasks persisted for a run, including agent task metadata when present.

```bash
curl http://localhost:8080/api/runs/run-id-123/tasks
```

Optional query parameter:

- `path` — return a single task payload by full path (for example
  `/pipeline/run-id-123/tasks/0-review`)

Each item includes:

- `path` — task storage path
- `payload.status`, `payload.elapsed`, `payload.started_at`
- `payload.stdout`, `payload.stderr`, `payload.code`
- `payload.dependsOn`, `payload.type`
- `payload.usage`, `payload.toolCalls`, `payload.audit_log` for agent runs

See [MCP](./mcp.md) for advanced task search and filtering.
