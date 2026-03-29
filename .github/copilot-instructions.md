# PocketCI Project — Coding Agent Instructions

> Trust these instructions. Only search the codebase if information here is
> incomplete or found to be in error.

## Project Summary

PocketCI is a local-first CI/CD runtime written in Go
(`github.com/jtarchie/pocketci`). Pipelines are authored in JavaScript or
TypeScript and executed inside a Goja VM. The project also supports Concourse
YAML pipelines via a built-in transpiler. It features pluggable container
drivers (docker, native, k8s, fly, digitalocean, hetzner, qemu, vz), SQLite
storage, and an Echo HTTP server with an HTMx + idiomorph UI.

**Size:** ~50k lines Go, ~2k TypeScript, 284 Go source files across ~30
packages. **Module:** `github.com/jtarchie/pocketci`. **Go version:** 1.25+ (see
`go.mod`). **License:** MIT.

## Prerequisites

`brew bundle` installs all required tools from `Brewfile`: **Go 1.25+**,
**go-task**, **deno** (v2.x), **shellcheck**, **shfmt**, **minio**. Node.js +
npm are also required (not in Brewfile — install separately). **Docker must be
running** for integration tests.

## Build, Test, Lint — Command Reference

All commands must be run from the repo root. The build system is
[go-task](https://taskfile.dev) (`Taskfile.yml`).

### Step 1: Always build first

```bash
task build
```

This runs `task build:static` (npm install + build in `server/static/`),
`task
build:docs` (npm install + VitePress build in `docs/`),
`task build:backwards` (npm install in `backwards/`), and `go generate ./...`
(bundles `backwards/src/*.ts` into `backwards/bundle.js` via esbuild). **You
must run this before testing or linting.** Skipping it causes embed failures and
stale bundles.

### Step 2: Lint and format

```bash
task fmt
```

Runs deno fmt, deno lint, deno check, shellcheck, shfmt, gofmt, and
golangci-lint (with `--fix` locally, without in CI). Linter config is in
`.golangci.yml` (v2 format). Key enabled linters: errcheck, govet, staticcheck,
errorlint, musttag, sloglint, perfsprint, cyclop (max 15), gocognit (min 30),
contextcheck, containedctx.

To run golangci-lint standalone: `golangci-lint run ./...`

### Step 3: Run tests

```bash
go test -race ./... -count=1 -parallel=1
```

**Always use all three flags** (`-race`, `-count=1`, `-parallel=1`) — omitting
any of them causes flaky results. Single package example:

```bash
go test -race ./storage/... -count=1 -parallel=1
```

### Step 4: Cleanup after test failures

```bash
task cleanup
```

Runs `bin/cleanup.sh` to remove leaked Docker containers and volumes. Always run
this after test failures that may leave containers behind.

### Full CI (build + lint + test + e2e)

```bash
task
```

This is equivalent to `task build && task fmt && task test` (which includes
cleanup and e2e). The GitHub Actions CI has a **15-minute timeout**.

### Replicate CI locally

```bash
task build && task fmt && go test -race ./... -count=1 -parallel=1
```

## Critical Build Rules

1. **After editing `backwards/src/*.ts`**: always run `go generate ./...` to
   regenerate `backwards/bundle.js` — it is `//go:embed`-ed and stale bundles
   cause silent failures.
2. **After editing `server/static/src/`**: always run `task build:static`.
3. **After editing `docs/**/*.md` or `docs/.vitepress/`**: always run
   `task
   build:docs`.
4. **After editing `storage/sqlite/schema.sql`**: run tests (schema is directly
   embedded).
5. **After editing `server/templates/`**: no rebuild needed (directly embedded
   at runtime).

## Project Layout

| Path             | Purpose                                                                                     |
| ---------------- | ------------------------------------------------------------------------------------------- |
| `main.go`        | CLI entry (kong). Blank imports register plugins                                            |
| `commands/`      | CLI subcommands: server, pipeline (set/rm/ls/run/pause/unpause), resource, login            |
| `runtime/`       | Goja VM engine. `js.go` (TS->JS), `runtime.go` (API), `jsapi/`, `runner/`                   |
| `orchestra/`     | Container layer. `orchestrator.go` (interfaces), `docker/`, `native/`, `k8s/`, `fly/`, etc. |
| `storage/`       | Persistence. `storage.go` (Driver interface), `sqlite/` (schema.sql embedded)               |
| `backwards/`     | Concourse YAML -> JS transpiler. `pipeline.go` (go:generate), `src/` (TS)                   |
| `server/`        | Echo HTTP + HTMx. `router.go`, `templates.go`, `templates/`, `static/`                      |
| `secrets/`       | Secrets manager (Get/Set/Delete/ListByScope) + backends                                     |
| `webhooks/`      | Webhook providers (generic, github, slack, honeybadger)                                     |
| `resources/`     | Concourse-compat resource interface + mock                                                  |
| `examples/`      | Pipeline examples + integration tests (`examples_test.go`)                                  |
| `testhelpers/`   | Test utils: `Runner` (pipeline executor), `StartMinIO` (S3 tests)                           |
| `e2e/`           | Playwright browser tests                                                                    |
| `observability/` | Telemetry: honeybadger, OpenTelemetry tracing                                               |
| `cache/`         | Caching implementation                                                                      |
| `executor/`      | Pipeline executor                                                                           |
| `s3config/`      | S3 configuration                                                                            |

### Key configuration files

| File                       | Purpose                                        |
| -------------------------- | ---------------------------------------------- |
| `Taskfile.yml`             | All build/test/lint tasks                      |
| `.golangci.yml`            | Linter configuration (golangci-lint v2 format) |
| `go.mod`                   | Go module + dependency versions                |
| `Brewfile`                 | Homebrew tool dependencies                     |
| `.github/workflows/go.yml` | CI pipeline definition                         |
| `fly.toml`                 | Fly.io deployment config                       |
| `.goreleaser.yml`          | Release pipeline config                        |

## Plugin Registration

All plugins self-register via `init()` + blank imports. Pattern: the driver
package defines `func init() { /* register */ }`, and `main.go` (or test files)
imports with `_ "github.com/jtarchie/pocketci/orchestra/docker"`. New plugins:
implement the `orchestra.Driver` interface, add `init()`, add blank import.

**In test files**, you must add blank imports for the drivers you need:

```go
import (
    _ "github.com/jtarchie/pocketci/orchestra/docker"
    _ "github.com/jtarchie/pocketci/orchestra/native"
)
```

## Development Practices

### Tests (required for every change)

- **Black-box packages**: always `package foo_test` (test public API only).
- **Assertions**: gomega — `assert := NewGomegaWithT(t)`,
  `assert.Expect(err).NotTo(HaveOccurred())`.
- **In-memory DB**: always use `sqlite://:memory:` (never file-backed unless
  testing persistence).
- **Table-driven tests**: use `Each()` from `storage`/`orchestra`/`secrets`
  packages.
- **Helpers**: `testhelpers.Runner` for pipelines, `testhelpers.StartMinIO` for
  S3 tests. `t.TempDir()` for temp files, `t.Cleanup()` for teardown.
- **Goroutine leaks**: tests use `go.uber.org/goleak` — ensure goroutines are
  cleaned up.

### Logging

Use `slog` everywhere — never `log` or `fmt.Println`. Pass `*slog.Logger` via
parameters. In tests: `slog.New(slog.NewTextHandler(io.Discard, nil))`.

- **Messages**: dot-separated names: `"pipeline.validate.success"`,
  `"image.pull"`.
- **Typed attrs**: prefer `slog.String()`, `slog.Int()`, `slog.Duration()`.

### Errors and JSON tags

- Wrap errors with context: `fmt.Errorf("context: %w", err)`.
- All structs exposed to the Goja VM must have `json:"fieldName"` tags (enforced
  by `musttag` linter).

### Key interfaces

- `orchestra.Driver` — container orchestration
- `storage.Driver` — data persistence
- `secrets.Manager` — secrets management
- `webhooks.Provider` — webhook integrations
- Interface compliance: `var _ orchestra.Driver = &Docker{}`

## CI Validation

PR gate (`.github/workflows/go.yml`): runs on `ubuntu-latest` with Docker,
minikube, Node, Deno v2.x, Go stable. Steps: `task build:static` ->
`task
build:docs` -> `golangci-lint` -> `task` (full CI). **15-minute timeout.**

Before submitting, always validate locally:

```bash
task
```
