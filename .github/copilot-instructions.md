# PocketCI Project — Coding Agent Instructions

> Trust these instructions. Only search the codebase if information here is
> incomplete or found to be in error.

## Project Summary

Local-first CI/CD runtime in Go (`github.com/jtarchie/pocketci`). JS/TS
pipelines via Goja VM, Concourse YAML backward compat. Pluggable container
drivers (docker, native, k8s, fly, digitalocean, hetzner, qemu, vz), SQLite
storage, Echo HTTP server with HTMx + idiomorph UI. ~50k lines Go, ~2k TS.

## Prerequisites

`brew bundle` installs all tools. Required: **Go 1.25+**, **go-task**, **deno**
(v2.x), **Node.js + npm**, **shellcheck**, **shfmt**. **Docker must be running**
for integration tests. Versions come from `go.mod` and `Brewfile`.

## Build, Test, Lint

Always run from repo root. Task runner: [go-task](https://taskfile.dev)
(`Taskfile.yml`).

| Command                                    | Purpose                                                                     |
| ------------------------------------------ | --------------------------------------------------------------------------- |
| `task build`                               | **Always run first.** Builds static assets, docs, runs `go generate ./...`  |
| `task fmt`                                 | Lint & format: deno fmt/lint/check, shellcheck, shfmt, gofmt, golangci-lint |
| `go test -race ./... -count=1 -parallel=1` | **Always use all three flags** — omitting causes flaky results              |
| `task cleanup`                             | Remove leaked Docker containers/volumes. Run after test failures            |
| `task`                                     | Full CI: build → fmt → cleanup → test → e2e                                 |

Single package: `go test -race ./storage/... -count=1 -parallel=1`. No
`.golangci.yml` — golangci-lint uses default rules.

## Critical Build Rules

1. **After editing `backwards/src/*.ts`**: run `go generate ./...` to regenerate
   `backwards/bundle.js` (`//go:embed`-ed — stale bundles cause silent
   failures).
2. **After editing `server/static/src/`**: run `task build:static`.
3. **After editing `docs/**/\*.md`or`docs/.vitepress/`**: run `task build:docs`.
4. **After editing `storage/sqlite/schema.sql`**: run tests (directly embedded).
5. **After editing `server/templates/`**: no rebuild needed (directly embedded).

## Project Layout

| Path           | Purpose                                                                                                              |
| -------------- | -------------------------------------------------------------------------------------------------------------------- |
| `main.go`      | CLI entry (kong). Blank imports register all plugins                                                                 |
| `commands/`    | CLI subcommands: server, pipeline (set/rm/ls/run/pause/unpause), resource, login                                     |
| `runtime/`     | Goja VM engine. `js.go` (TS→JS), `runtime.go` (API), `jsapi/` (helpers), `runner/`                                   |
| `orchestra/`   | Container layer. `orchestrator.go` (interfaces), `drivers.go` (registry), `docker/`, `native/`, `k8s/`, `fly/`, etc. |
| `storage/`     | Persistence. `storage.go` (Driver interface), `sqlite/` (schema.sql embedded)                                        |
| `backwards/`   | Concourse YAML → JS transpiler. `pipeline.go` (go:generate), `src/` (TS), `config.go`, `validation/`                 |
| `server/`      | Echo HTTP + HTMx. `router.go`, `templates.go` (embeds), `templates/`, `static/`, `docs/site/`                        |
| `secrets/`     | Secrets manager (Get/Set/Delete/ListByScope) + backends                                                              |
| `webhooks/`    | Webhook providers (generic, github, slack, honeybadger)                                                              |
| `resources/`   | Concourse-compat resource interface + mock                                                                           |
| `examples/`    | Pipeline examples + integration tests (`examples_test.go`)                                                           |
| `testhelpers/` | Test utils: `Runner` (pipeline executor), `StartMinIO` (S3 tests)                                                    |
| `e2e/`         | Playwright browser tests                                                                                             |

## Plugin Registration

All plugins self-register via `init()` + blank imports in `main.go`:
`func init() { orchestra.Add("docker", NewDocker) }` in the driver package,
`_ "github.com/jtarchie/pocketci/orchestra/docker"` in `main.go`. New plugins:
implement the interface, add `init()`, add blank import to `main.go`.

## Development Practices

### Always Write Tests

Every change must include tests — no exceptions.

- **Black-box packages**: always `package foo_test` (public API only).
- **Assertions**: gomega — `assert := NewGomegaWithT(t)`,
  `assert.Expect(err).NotTo(HaveOccurred())`.
- **In-memory DB**: `sqlite://:memory:` (never file-backed unless testing
  persistence).
- **Driver imports**: `_ "github.com/jtarchie/pocketci/orchestra/docker"`
  (and/or `/native`).
- **Table-driven**: use `Each()` from `storage`/`orchestra`/`secrets` packages.
- **Helpers**: `testhelpers.Runner` for pipelines, `testhelpers.StartMinIO` for
  S3 tests. `t.TempDir()` for temp files, `t.Cleanup()` for teardown.

### Single Responsibility Principle

Create packages with clear interfaces for domain boundaries (`storage.Driver`,
`orchestra.Driver`, `secrets.Manager`, `webhooks.Provider`). Follow the plugin
registration pattern for new implementations. Interface compliance checks:
`var _ orchestra.Driver = &Docker{}`.

### Logging

Use `slog` everywhere — never `log` or `fmt.Println`. Pass `*slog.Logger` via
parameters. In tests: `slog.New(slog.NewTextHandler(io.Discard, nil))`.

- **Levels**: `Info` (operations), `Debug` (internals), `Error` (failures),
  `Warn` (recoverable).
- **Groups**: `logger.WithGroup("component").With("key", value)`.
- **Messages**: dot-separated names: `"pipeline.validate.success"`,
  `"image.pull"`.
- **Typed attrs**: prefer `slog.String()`, `slog.Int()`, `slog.Duration()`.

### Errors & JSON Tags

- Wrap errors: `fmt.Errorf("context: %w", err)`.
- All structs exposed to Goja VM must have `json:"fieldName"` tags.

### UI

HTMx + idiomorph (`morph:innerHTML`/`morph:outerHTML`). Semantic HTML + ARIA. No
custom JS for DOM state.

## CI Validation

PR gate (`.github/workflows/go.yml`): `ubuntu-latest`, Docker, minikube, Node,
Deno v2.x, Go stable. Steps: `task build:static` → `task build:docs` →
`golangci-lint` → `task` (default). **15-minute timeout.** Replicate locally:

```bash
task build && task fmt && go test -race ./... -count=1 -parallel=1
```
