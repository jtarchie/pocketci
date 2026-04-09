# CI/CD in Production: Fly.io Deployment & Multi-Stage Pipelines

This guide takes you from a bare Fly.io account to a working CI/CD pipeline that
automatically lints, tests, and builds your Go project on every push to `main`.
PocketCI runs on Fly.io so GitHub can reach it via webhook.

**What you'll build:**

- PocketCI server deployed to Fly.io with basic auth
- Three-job pipeline: lint → test → build (lint and test run in parallel)
- GitHub webhook trigger filtered to `main` branch pushes
- Optional: nightly schedule to catch dependency drift

**Prerequisites:** [flyctl](https://fly.io/docs/hands-on/install-flyctl/)
installed and authenticated, plus a GitHub repository with a Go project.

## 1. Deploy PocketCI to Fly.io

Create the app and a persistent volume for the SQLite database, then set your
credentials as Fly secrets before deploying so they never appear in the process
list:

```bash
fly apps create my-pocketci
fly volumes create ci_data --region sjc --size 1

fly secrets set \
  CI_BASIC_AUTH="admin:$(openssl rand -base64 16)" \
  CI_SECRETS_SQLITE_PASSPHRASE="$(openssl rand -base64 32)"

flyctl deploy \
  --image ghcr.io/jtarchie/pocketci:latest \
  --env CI_STORAGE_SQLITE_PATH=/data/pocketci.db \
  --env CI_SECRETS_SQLITE_PATH=/data/pocketci.db
```

Your server is now live at `https://my-pocketci.fly.dev`, protected by basic
auth. Open the web UI to confirm:

<a href="/screenshots/production/01-server-running.png" target="_blank">
  <img src="/screenshots/production/01-server-running.png" alt="PocketCI server running on Fly.io — empty pipelines list" />
</a>

## 2. Write the Pipeline

Create `ci.yml` in your project repository. The pipeline has three jobs: `lint`
and `test` run in parallel when a commit lands, then `build` runs only after
both pass.

```yaml
resources:
  - name: source
    type: git
    source:
      uri: https://github.com/my-org/my-app
      branch: main

jobs:
  - name: lint
    webhook_trigger: 'provider == "github" && payload.ref == "refs/heads/main"'
    plan:
      - get: source
        trigger: true
      - task: golangci-lint
        config:
          platform: linux
          image_resource:
            type: registry-image
            source:
              repository: golangci/golangci-lint
          inputs:
            - name: source
          run:
            dir: source
            path: golangci-lint
            args: [run, ./...]

  - name: test
    webhook_trigger: 'provider == "github" && payload.ref == "refs/heads/main"'
    plan:
      - get: source
        trigger: true
      - task: go-test
        config:
          platform: linux
          image_resource:
            type: registry-image
            source:
              repository: golang
              tag: "1.25"
          inputs:
            - name: source
          caches:
            - path: /root/go/pkg/mod
          run:
            dir: source
            path: go
            args: [test, -race, ./...]

  - name: build
    plan:
      - get: source
        passed: [lint, test]
      - task: go-build
        config:
          platform: linux
          image_resource:
            type: registry-image
            source:
              repository: golang
              tag: "1.25"
          inputs:
            - name: source
          caches:
            - path: /root/go/pkg/mod
          run:
            dir: source
            path: go
            args: [build, -o, bin/app, .]
```

Key patterns in this pipeline:

- **`resources`** — the `git` resource fetches your source code. Each job that
  does `get: source` gets its own clean checkout.
- **`trigger: true`** — lint and test start automatically when a new commit is
  detected on the `source` resource.
- **`webhook_trigger`** — filters which webhook payloads activate each job. Only
  pushes to `main` are processed; pull request events and other branches are
  ignored.
- **`caches`** — the Go module cache at `/root/go/pkg/mod` persists across runs.
  Configure [S3-backed caching](../operations/caching.md) to share it across
  machines.
- **`passed: [lint, test]`** — the build job only runs after both lint and test
  complete successfully. If either fails, build is skipped.

## 3. Register the Pipeline

Log in, then register `ci.yml`. The `--driver fly` flag tells PocketCI to spin
up a fresh Fly.io machine for each pipeline run — no shared state between runs:

```bash
pocketci login --server-url https://my-pocketci.fly.dev

pocketci pipeline set ci.yml \
  --server-url https://my-pocketci.fly.dev \
  --name my-app-ci \
  --driver fly \
  --fly-token $(fly auth token) \
  --fly-app my-ci-runners \
  --fly-region sjc \
  --webhook-secret $(openssl rand -hex 32)
```

The `--webhook-secret` value is used to validate the HMAC-SHA256 signature on
every incoming webhook request. Copy it — you'll need it in the next step.

<a href="/screenshots/production/02-pipeline-registered.png" target="_blank">
  <img src="/screenshots/production/02-pipeline-registered.png" alt="Pipeline registered and visible in the PocketCI web UI" />
</a>

## 4. Wire Up the GitHub Webhook

Get your pipeline's webhook URL:

```bash
pocketci pipeline ls --server-url https://my-pocketci.fly.dev
```

The output includes a webhook URL in the form
`https://my-pocketci.fly.dev/api/webhooks/<pipeline-id>`.

In your GitHub repository, go to **Settings → Webhooks → Add webhook**:

- **Payload URL** — paste the webhook URL
- **Content type** — `application/json`
- **Secret** — paste the `--webhook-secret` value from step 3
- **Events** — select "Just the push event"

Save the webhook. The next push to `main` will trigger lint and test in
parallel, followed by build if both pass.

<a href="/screenshots/production/03-run-success.png" target="_blank">
  <img src="/screenshots/production/03-run-success.png" alt="Completed pipeline run showing lint, test, and build all succeeded" />
</a>

## 5. Add a Nightly Schedule (Optional)

Add a `triggers.schedule` block to run the test job every night at 2am UTC, even
without a push. This catches flaky tests and dependency drift:

```yaml
jobs:
  - name: test
    triggers:
      schedule:
        cron: "0 2 * * *"
    webhook_trigger: 'provider == "github" && payload.ref == "refs/heads/main"'
    plan:
      - get: source
      # ... rest of the job
```

The schedule and webhook trigger are independent — the job runs on either
condition.

Enable the `schedules` feature on your Fly.io deployment:

```bash
fly secrets set CI_ALLOWED_FEATURES="*,schedules"
```

See [Scheduling](./scheduling.md) for interval-based triggers and multi-instance
scheduling behavior.

## What's Next

- [Secrets Management](../operations/secrets.md) — inject API keys and tokens
  into pipeline steps without storing them in `ci.yml`
- [Webhooks](./webhooks.md) — handle pull request events, filter by branch or
  author, and respond back to GitHub
- [Caching](../operations/caching.md) — connect an S3 bucket to share the Go
  module cache across all Fly.io machines
- [Authentication](../operations/authentication.md) — upgrade from basic auth to
  GitHub OAuth for team access
