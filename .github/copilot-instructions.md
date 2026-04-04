# PocketCI — Coding Agent Instructions

> Trust these instructions. Only search the codebase if information here is
> incomplete or found to be in error.

## Project Summary

PocketCI is a local-first CI/CD runtime written in Go
(`github.com/jtarchie/pocketci`). Pipelines are authored in
JavaScript/TypeScript and executed inside a Goja VM. Supports Concourse YAML
pipelines via a built-in transpiler. Features pluggable container drivers
(docker, native, k8s, fly, digitalocean, hetzner, qemu, vz), SQLite storage, and
an Echo HTTP server with HTMx + idiomorph UI.

**Size:** ~50k lines Go, ~2k TypeScript, 284 Go source files, ~30 packages. **Go
version:** 1.25.8. **Module:** `github.com/jtarchie/pocketci`. **License:** MIT.

## Build, Test, Lint

All commands run from the repo root. Build system:
[go-task](https://taskfile.dev) (`Taskfile.yml`).

### Step 1: Always build first

```bash
task build
```

Runs in order: (1) `cd server/static && npm install && npm run build`, (2)
`cd docs && npm install && npm run build`, (3) `go generate ./...` (bundles
`backwards/src/*.ts` → `backwards/bundle.js` via esbuild, which is
`//go:embed`-ed). **Skipping causes embed failures and stale bundles.**

### Step 2: Lint and format

```bash
task fmt
```

Runs deno fmt/lint/check, shellcheck, shfmt, gofmt, and
`golangci-lint run ./... --fix` (only when `CI != "true"` — in CI it runs via
the golangci-lint action without `--fix`). Linter config: `.golangci.yml` (v2
format).

### Step 3: Run tests

```bash
go test -race ./... -count=1 -parallel=1
```

Always use all three flags — omitting any causes flaky results. Single package:
`go test -race ./storage/... -count=1 -parallel=1`. Docker must be running.

### Full CI (build + lint + test + e2e)

```bash
task
```

Equivalent to `task build && task fmt && task test` (test includes cleanup +
Playwright e2e). **15-minute timeout in CI.**

### Cleanup after test failures

```bash
task cleanup
```

Removes leaked Docker containers/volumes. Always run after test failures.

## Critical Rebuild Rules

| Changed files                        | Required action                                                  |
| ------------------------------------ | ---------------------------------------------------------------- |
| `backwards/src/*.ts`                 | `go generate ./...` (regenerates embedded `backwards/bundle.js`) |
| `server/static/src/`                 | `task build:static`                                              |
| `docs/**/*.md` or `docs/.vitepress/` | `task build:docs` (docs build uses `deadLinks: "error"`)         |
| `storage/sqlite/schema.sql`          | run tests (schema is directly embedded)                          |
| `server/templates/`                  | no rebuild needed (embedded at runtime)                          |

## Project Layout

| Path             | Purpose                                                                                     |
| ---------------- | ------------------------------------------------------------------------------------------- |
| `main.go`        | CLI entry (kong). Blank-imports `resources/mock` only                                       |
| `commands/`      | CLI subcommands: server, pipeline (set/rm/ls/run/pause/unpause), resource, login            |
| `runtime/`       | Goja VM engine: `js.go` (TS→JS), `runtime.go` (API), `jsapi/`, `runner/`                    |
| `orchestra/`     | Container layer: `orchestrator.go` (interfaces), `docker/`, `native/`, `k8s/`, `fly/`, etc. |
| `storage/`       | Persistence: `storage.go` (Driver interface), `sqlite/` (schema.sql embedded)               |
| `backwards/`     | Concourse YAML→JS transpiler: `pipeline.go` (go:generate), `src/` (TS)                      |
| `server/`        | Echo HTTP + HTMx: `router.go`, `templates/`, `static/`                                      |
| `secrets/`       | Secrets manager (Get/Set/Delete/ListByScope) + backends                                     |
| `testhelpers/`   | Test utils: `Runner` (pipeline executor), `StartMinIO` (S3 tests)                           |
| `examples/`      | Pipeline examples + integration tests (`examples_test.go`)                                  |
| `e2e/`           | Playwright browser tests                                                                    |
| `observability/` | Telemetry: honeybadger, OpenTelemetry tracing                                               |

**Key config files:** `Taskfile.yml` (all tasks), `.golangci.yml` (linter),
`go.mod` (deps), `.github/workflows/go.yml` (CI).

## Driver Registration

Drivers are **not** self-registering via `init()`. They are wired via a
hard-coded switch statement in `server/create_driver.go`. To add a new driver:
implement `orchestra.Driver`, add a case to both switches in
`server/create_driver.go` (driver creation and config unmarshalling), and add
the import there.

In test files that exercise a specific driver, add blank imports for the drivers
needed:

```go
import (
    _ "github.com/jtarchie/pocketci/orchestra/docker"
    _ "github.com/jtarchie/pocketci/orchestra/native"
)
```

## Code Conventions

**Tests:** Always `package foo_test` (black-box). Use gomega:
`assert := NewGomegaWithT(t)`, `assert.Expect(err).NotTo(HaveOccurred())`. Use
`sqlite://:memory:` for DB. Use `testhelpers.Runner` for pipeline tests. Use
`t.TempDir()` and `t.Cleanup()`. Tests use `go.uber.org/goleak` — ensure
goroutines are cleaned up.

**Logging:** `slog` everywhere — never `log` or `fmt.Println`. Dot-separated
message names: `"pipeline.validate.success"`. Use typed attrs: `slog.String()`,
`slog.Int()`, `slog.Duration()`. In tests:
`slog.New(slog.NewTextHandler(io.Discard, nil))`.

**Errors and JSON tags:** Wrap with `fmt.Errorf("context: %w", err)`. All
structs exposed to the Goja VM **must** have `json:"fieldName"` tags (enforced
by `musttag` linter — missing tags break marshalling silently).

**Linter suppressions:** Prefer fixing root causes over `//nolint`. When
unavoidable, always include a reason: `//nolint:rulename // reason`. Use
`t.Setenv()`/`t.TempDir()` (not `os.Setenv`/`os.MkdirTemp`) in tests. Use
comma-ok type assertions. Use `http.NewRequestWithContext`. Log or capture
deferred `Close()` errors.

## Documentation Requirements

Every user-facing change must include doc updates: new CLI flags → `docs/cli/`,
new API endpoints → `docs/api/`, new JS/TS runtime API → `docs/runtime/`. After
adding pages: add to sidebar in `docs/.vitepress/config.ts` and link from the
index. Run `task build:docs` to verify (broken links are errors).

## CI Validation

PR gate (`.github/workflows/go.yml`): ubuntu-latest with Docker + minikube, Deno
v2.x, Go stable. Sequence: `task build:static` → `task build:docs` →
golangci-lint action → `task`. **15-minute timeout.**

To replicate locally:

```bash
task build && task fmt && go test -race ./... -count=1 -parallel=1
```
