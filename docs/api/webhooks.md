# Webhooks API

Trigger pipelines via HTTP webhooks.

`POST /api/webhooks/:pipeline-id`

Execute a pipeline in response to an HTTP request. The pipeline can read the
incoming request and optionally send an HTTP response before continuing
execution in the background.

## Signature Validation (Optional)

The server automatically detects the webhook provider from the request headers
and applies provider-specific signature verification.

| Provider      | Detection header        | Signature header                                            |
| ------------- | ----------------------- | ----------------------------------------------------------- |
| `github`      | `X-GitHub-Event`        | `X-Hub-Signature-256: sha256=<hex>`                         |
| `gitlab`      | `X-Gitlab-Event`        | `X-Gitlab-Token: sha256=<hex>` or plain token               |
| `bitbucket`   | `X-Event-Key`           | `X-Hub-Signature: sha256=<hex>`                             |
| `slack`       | `X-Slack-Signature`     | `X-Slack-Signature: v0=<hex>` + `X-Slack-Request-Timestamp` |
| `honeybadger` | `Honeybadger-Token`     | `Honeybadger-Token: <token>` (required)                     |
| `stripe`      | `Stripe-Signature`      | `Stripe-Signature: t=<ts>,v1=<hex>`                         |
| `pagerduty`   | `X-PagerDuty-Signature` | `X-PagerDuty-Signature: v1=<hex>[,v1=<hex>]`                |
| `linear`      | `Linear-Signature`      | `Linear-Signature: <hex>`                                   |
| `sentry`      | `Sentry-Hook-Signature` | `Sentry-Hook-Signature: <hex>`                              |
| `generic`     | _(fallback)_            | `X-Webhook-Signature: <hex>` or `?signature=<hex>`          |

If the pipeline has a `webhook_secret` configured, requests must pass the
provider's signature check. Requests that fail validation receive
`401 Unauthorized`.

## Example

```bash
curl -X POST http://localhost:8080/api/webhooks/my-pipeline \
  -H "Content-Type: application/json" \
  -H "X-Webhook-Signature: ..." \
  -d '{"event": "push", "branch": "main"}'
```

## Pipeline API

Inside the pipeline, access the incoming request and respond:

```typescript
const pipeline = async () => {
  const req = http.request();
  if (req) {
    // req.provider    — "github" | "gitlab" | "bitbucket" | "slack" | "honeybadger" | "stripe" | "pagerduty" | "linear" | "sentry" | "generic"
    // req.eventType   — e.g. "push", "pull_request", "event_callback"
    http.respond({
      status: 200,
      body: JSON.stringify({ acknowledged: true, provider: req.provider }),
      headers: { "Content-Type": "application/json" }
    });
  }

  // Pipeline continues running in the background
  await runtime.run({ ... });
};
export { pipeline };
```

### Conditional execution — `webhookTrigger(expression)`

Gate execution on webhook metadata using an
[expr-lang](https://expr-lang.github.io/expr/) boolean expression. Returns
`true` for manual (non-webhook) runs so jobs are never silently skipped.

```typescript
// Only run when a GitHub push event targets main
if (
  !webhookTrigger(
    'provider == "github" && eventType == "push" && payload.ref == "refs/heads/main"',
  )
) {
  return;
}
```

Available variables: `provider`, `eventType`, `method`, `headers`, `query`,
`body`, `payload` (JSON-decoded body, or `nil`).

**YAML pipelines** use the `webhook_trigger` field on a job:

```yaml
jobs:
  - name: deploy
    webhook_trigger: 'provider == "github" && eventType == "push"'
    plan:
      - task: deploy
        ...
```

When the expression returns `false` the job is recorded as `"skipped"`. Manual
triggers bypass the filter entirely.

See [Webhooks guide](../guides/webhooks.md) for detailed examples (GitHub,
Slack, conditional execution, custom signatures, etc.).
