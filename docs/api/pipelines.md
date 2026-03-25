# Pipelines API

Manage stored pipelines on the server.

## List Pipelines

`GET /api/pipelines`

Returns a paginated list of pipelines.

```bash
curl http://localhost:8080/api/pipelines
```

## Create or Update Pipeline

`PUT /api/pipelines/:name`

Store a new pipeline or update an existing one.

```bash
curl -X PUT http://localhost:8080/api/pipelines/my-pipeline \
  -H "Content-Type: application/json" \
  -d '{
    "content": "export const pipeline = async () => { console.log(\"test\"); };",
    "driver": "docker",
    "driver_config": {"host": "tcp://remote:2376"},
    "webhook_secret": "optional-secret"
  }'
```

## Get Pipeline

`GET /api/pipelines/:name`

Retrieve a specific pipeline's metadata and source.

```bash
curl http://localhost:8080/api/pipelines/my-pipeline
```

## Delete Pipeline

`DELETE /api/pipelines/:name`

Remove a pipeline.

```bash
curl -X DELETE http://localhost:8080/api/pipelines/my-pipeline
```

## Trigger Pipeline

`POST /api/pipelines/:name/run`

Execute a stored pipeline and stream output back as Server-Sent Events.

```bash
curl -X POST http://localhost:8080/api/pipelines/my-pipeline/run \
  -H "Content-Type: application/json" \
  -d '{"args": ["arg1", "arg2"]}'
```

Response is an SSE stream with events:

- `{"stream":"stdout","data":"..."}` — container stdout
- `{"stream":"stderr","data":"..."}` — container stderr
- `{"event":"exit","code":0,"run_id":"..."}` — pipeline finished
- `{"event":"error","message":"..."}` — fatal error

See [pocketci pipeline-run](../cli/pipeline-run.md) for client-side usage.

### Quick Trigger

`POST /api/pipelines/:id/trigger`

Fire-and-forget trigger used by the web UI. Accepts an optional JSON body:

| Field             | Type       | Description                                              |
| ----------------- | ---------- | -------------------------------------------------------- |
| `mode`            | `string`   | `"manual"` (default), `"args"`, or `"webhook"`           |
| `args`            | `string[]` | Arguments passed to `pipelineContext.args` (mode `args`) |
| `webhook.method`  | `string`   | HTTP method for simulated webhook (mode `webhook`)       |
| `webhook.headers` | `object`   | Headers for simulated webhook                            |
| `webhook.body`    | `string`   | JSON body for simulated webhook                          |

Webhook mode requires a `webhook_secret` configured on the pipeline; returns
`403` otherwise.
