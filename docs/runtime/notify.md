# notify

Send notifications from pipelines to external services.

```typescript
await notify.send({ name: "my-slack", message: "Build {{ .Status }}" });
```

## Setup

Configure backends and optional pipeline context before sending:

```typescript
notify.configure({
  backends: {
    "my-slack": {
      type: "slack",
      token: "secret:SLACK_TOKEN",
      channels: ["#builds"],
    },
  },
  context: {
    pipelineName: pipelineContext.name,
    status: "running",
  },
});
```

## notify.configure(input)

Set backends and pipeline context in a single call.

- `backends` (optional) — map of named `NotifyConfig` objects
- `context` (optional) — `NotifyContext` fields to set

## notify.send(input)

Send a notification to a named backend. Returns a Promise.

- `name` — backend name (must match a key from `backends`)
- `message` — Go template string rendered against the current context (supports
  most [Sprig functions](../runtime/templating.md); `env` and `expandenv` are
  not available for security reasons)
- `async` (optional) — fire-and-forget; resolves immediately without waiting for
  delivery

```typescript
const result = await notify.send({
  name: "my-slack",
  message: "Pipeline {{ .PipelineName }} finished: {{ .Status | upper }}",
});

if (!result.success) {
  console.error("notification failed:", result.error);
}
```

## notify.sendMultiple(input)

Send the same message to multiple backends at once. Returns a Promise.

- `names` — array of backend names
- `message` — template string
- `async` (optional) — fire-and-forget mode

## notify.updateContext(partial)

Apply a partial update to the current context. Only non-empty fields overwrite
existing values.

```typescript
notify.updateContext({ status: "success", endTime: new Date().toISOString() });
```

## NotifyConfig

All fields support `secret:KEY` references for credentials (see
[Secrets](../operations/secrets.md)).

### Slack

```typescript
{
  type: "slack",
  token: "secret:SLACK_TOKEN",   // bot token (xoxb-...)
  channels: ["#ci"],             // channel names or IDs
  recipients: [],                // additional user IDs
}
```

### Microsoft Teams

```typescript
{
  type: "teams",
  webhook: "secret:TEAMS_WEBHOOK", // incoming webhook URL
}
```

### HTTP webhook

```typescript
{
  type: "http",
  url: "https://example.com/hook",
  method: "POST",                // optional, defaults to POST
  headers: {
    "Authorization": "secret:API_TOKEN",
  },
}
```

Payload sent as JSON:
`{ "subject": "Pipeline Notification", "message": "<rendered>" }`.

### Discord

```typescript
{
  type: "discord",
  token: "secret:DISCORD_BOT_TOKEN", // bot token
  channels: ["123456789"],            // channel IDs (not names)
  recipients: [],
}
```

### SMTP email

```typescript
{
  type: "smtp",
  smtpHost: "smtp.gmail.com:587",
  from: "ci@example.com",
  username: "ci@example.com",        // omit for unauthenticated relay
  token: "secret:SMTP_PASSWORD",     // password
  recipients: ["team@example.com"],
}
```

## NotifyContext

Fields available in message templates:

| Field          | Description                                         |
| -------------- | --------------------------------------------------- |
| `PipelineName` | Pipeline name                                       |
| `JobName`      | Current job name                                    |
| `BuildID`      | Unique build identifier                             |
| `Status`       | `pending`, `running`, `success`, `failure`, `error` |
| `StartTime`    | ISO timestamp                                       |
| `EndTime`      | ISO timestamp                                       |
| `Duration`     | Human-readable duration                             |
| `Environment`  | Map of environment variables                        |
| `TaskResults`  | Map of task output values                           |

## Full example

```typescript
const pipeline = async () => {
  notify.configure({
    backends: {
      slack: {
        type: "slack",
        token: "secret:SLACK_TOKEN",
        channels: ["#ci"],
      },
    },
    context: {
      pipelineName: pipelineContext.name,
      status: "running",
    },
  });

  await notify.send({
    name: "slack",
    message: "▶ {{ .PipelineName }} started",
  });

  try {
    await runtime.run({
      name: "build",
      image: "golang:1.22",
      command: { path: "go", args: ["build", "./..."] },
    });
    notify.updateContext({ status: "success" });
  } catch (e) {
    notify.updateContext({ status: "failure" });
    throw e;
  } finally {
    await notify.send({
      name: "slack",
      message:
        '{{ if eq .Status "success" }}✅{{ else }}❌{{ end }} {{ .PipelineName }}: {{ .Status }}',
    });
  }
};

export { pipeline };
```
