# Feature Gates

The CI server supports feature gates that let you control which pipeline
capabilities are available. By default, all features are enabled.

## Available Features

| Feature         | What it controls                                              |
| --------------- | ------------------------------------------------------------- |
| `webhooks`      | Webhook trigger routes, `http.request()`/`http.respond()` API |
| `secrets`       | `secret:` env var resolution and secret injection             |
| `notifications` | The `notify` system (Slack, Teams, HTTP)                      |
| `fetch`         | The global `fetch()` function for outbound HTTP requests      |
| `schedules`     | Background scheduler for cron/interval pipeline triggers      |
| `gates`         | Approval gates that pause pipeline execution until approved   |

> **Note:** The `schedules` and `gates` features are excluded from the `*`
> wildcard. To enable them alongside all default features, use
> `--allowed-features "*,schedules,gates"`.

## Configuration

### CLI Flag

```bash
pocketci server --allowed-features "webhooks,secrets"
```

### Environment Variable

```bash
export CI_ALLOWED_FEATURES="webhooks,secrets"
pocketci server
```

### Wildcard (default)

Use `*` to enable all features (this is the default):

```bash
pocketci server --allowed-features "*"
```

## Behavior

### Webhooks disabled

- `POST /api/pipelines` rejects requests that include a `webhook_secret`
- `ANY /api/webhooks/:id` returns `403 Forbidden`
- Pipeline execution does **not** receive webhook data or response channels

### Secrets disabled

- `secret:` prefixed env vars are **not** resolved during execution
- The secrets manager is not passed to the pipeline runtime

### Notifications disabled

- Calling `notify.send()` in a pipeline returns an error:
  `"notifications feature is not enabled"`

### Fetch disabled

- Calling `fetch()` in a pipeline returns an error:
  `"fetch feature is not enabled"`
- Outbound HTTP requests from pipelines are blocked

### Schedules disabled

- The scheduler background goroutine does not start
- Schedule declarations in YAML are still parsed and stored but never triggered
- `GET /api/pipelines/:id/schedules` still returns stored schedule data
- See [Scheduling](../guides/scheduling.md) for setup details

### Gates disabled

- `pipeline.gate()` calls in JavaScript pipelines will fail with an error
- Gate API endpoints (`/api/gates/*`, `/api/runs/:id/gates`) return
  `403 Forbidden`
- Gate declarations in YAML are still validated but not executed

## Discovery

Query the enabled features at runtime:

```bash
curl http://localhost:8080/api/features
# {"features":["webhooks","secrets","notifications","fetch"]}
```

## Error on unknown features

If you specify an unknown feature name, the server will refuse to start:

```
could not parse allowed features: unknown feature "bogus"; known features: webhooks, secrets, notifications, fetch, schedules, gates
```
