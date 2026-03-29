# Schedules API

Manage pipeline schedule triggers. Requires the `schedules`
[feature gate](../operations/feature-gates.md) to be enabled.

> **Note:** Schedules are created automatically when a YAML pipeline containing
> `triggers.schedule` is set via [`pipeline set`](../cli/pipeline-set.md). There
> is no endpoint for creating schedules directly.

## List Schedules

```
GET /api/pipelines/:id/schedules
```

Returns all schedules for the given pipeline.

### Response

```json
[
  {
    "id": "sch_abc123",
    "pipeline_id": "pl_xyz789",
    "name": "nightly-build",
    "schedule_type": "cron",
    "schedule_expr": "0 2 * * *",
    "job_name": "build",
    "enabled": true,
    "last_run_at": "2025-03-28T02:00:00Z",
    "next_run_at": "2025-03-29T02:00:00Z",
    "created_at": "2025-03-01T10:00:00Z",
    "updated_at": "2025-03-28T02:00:05Z"
  }
]
```

### Fields

| Field           | Type    | Description                                           |
| --------------- | ------- | ----------------------------------------------------- |
| `id`            | string  | Unique schedule identifier                            |
| `pipeline_id`   | string  | Parent pipeline ID                                    |
| `name`          | string  | Schedule name (unique per pipeline, derived from job) |
| `schedule_type` | string  | `"cron"` or `"interval"`                              |
| `schedule_expr` | string  | Cron expression or Go duration string                 |
| `job_name`      | string  | Target job name (empty string if whole pipeline)      |
| `enabled`       | boolean | Whether the schedule is active                        |
| `last_run_at`   | string  | ISO 8601 timestamp of last execution (null if never)  |
| `next_run_at`   | string  | ISO 8601 timestamp of next scheduled run              |
| `created_at`    | string  | ISO 8601 creation timestamp                           |
| `updated_at`    | string  | ISO 8601 last-update timestamp                        |

### Example

```bash
curl http://localhost:8080/api/pipelines/pl_xyz789/schedules
```

### Errors

- `404` — Pipeline not found

## Toggle Schedule Enabled

```
PUT /api/schedules/:id/enabled
```

Pause or unpause a schedule.

### Request Body

```json
{
  "enabled": false
}
```

### Response

```json
{
  "status": "ok"
}
```

### Example

Pause a schedule:

```bash
curl -X PUT http://localhost:8080/api/schedules/sch_abc123/enabled \
  -H "Content-Type: application/json" \
  -d '{"enabled": false}'
```

Unpause a schedule:

```bash
curl -X PUT http://localhost:8080/api/schedules/sch_abc123/enabled \
  -H "Content-Type: application/json" \
  -d '{"enabled": true}'
```

### Errors

- `400` — Invalid request body
- `404` — Schedule not found
- `500` — Internal server error
