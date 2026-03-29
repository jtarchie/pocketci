# Server API Reference

The CI server exposes a JSON API for pipeline and run management.

All endpoints support optional basic authentication (if configured).

## Endpoints by Category

- [Pipelines](./pipelines.md) — create, list, update, delete, trigger pipelines
- [Runs](./runs.md) — query execution history and task logs
- [Webhooks](./webhooks.md) — trigger pipelines via HTTP webhooks
- [Schedules](./schedules.md) — manage pipeline schedule triggers
- [Drivers](./drivers.md) — list available orchestration drivers
- [Features](./features.md) — list available feature gates
- [MCP](./mcp.md) — Model Context Protocol server for AI assistants

## Authentication

If `--basic-auth-username` and `--basic-auth-password` are set on the server,
include `Authorization: Basic <base64(user:pass)>` in requests.

Webhook endpoints do not require basic auth but may validate HMAC signatures
(see [Webhooks](./webhooks.md)).

## Response Format

All responses are JSON. Errors include an `error` field and an HTTP status code.

Standard error response:

```json
{
  "error": "pipeline not found"
}
```
