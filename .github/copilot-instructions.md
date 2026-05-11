# PocketCI — Agent Onboarding Instructions

Trust these instructions. Only fall back to repo-wide search if something here
is demonstrably wrong or missing.

## What this repo is

PocketCI is a **local-first CI/CD runtime**. Pipelines are written in
JavaScript/TypeScript (and YAML, for Concourse-CI backwards compatibility) and
executed inside containers/VMs by one of several orchestration **drivers**
(Docker, Kubernetes, Fly.io, DigitalOcean, Hetzner, QEMU, macOS VZ, native).
State is persisted to a single SQLite database. There is also a web server with
HTMX-driven UI, REST API, MCP server, and webhook receivers.

- Primary language: **Go 1.26** (`go.mod` →
  `module github.com/jtarchie/pocketci`). `main.go` wires the CLI via
  [alecthomas/kong](https://github.com/alecthomas/kong).
- Pipeline scripting language: **TypeScript/JavaScript**, executed in-process by
  [dop251/goja](https://github.com/dop251/goja) (a pure-Go ES5+ runtime).
  `runtime/jsapi/` exposes Go ↔ JS bindings.
- Front end: HTMX + Tailwind v4 + esbuild bundle in `server/static/`.
- Docs: VitePress in `docs/` (also embedded into the server at build time).
- E2E: Playwright in `e2e/`.
- Repo size: ~50k+ LoC of Go across ~30 top-level packages, plus a TS/Node front
  end and docs site. `go.sum` is large (~66 KB) — many third-party deps
  including Docker, AWS SDK v2, k8s client-go, fly.io machines, goja, esbuild,
  VZ, qemu.

The **CLAUDE.md** at the repo root is a symlink to **this file** — anything you
add here is what both Copilot and Claude Code agents will see.

## Toolchain (always required)

Install via Homebrew on macOS (`brew bundle` in repo root reads `Brewfile`) or
the equivalents on Linux. CI installs them via `setup-*` actions.

| Tool                                   | Where it's used                                                                                                                                                                                                   | Notes                                                                                                                    |
| -------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `go` (1.26+, `stable`)                 | All Go code                                                                                                                                                                                                       | `setup-go@v6 with go-version: stable, check-latest: true` in CI                                                          |
| `task` (go-task)                       | The single entry point for all build/test/lint                                                                                                                                                                    | `Taskfile.yml`. Always invoke commands through `task <name>`, not bare `go build`/`npm run` — tasks chain prerequisites. |
| `deno` (v2.x)                          | TS formatting, linting, type checking of `examples/`. Also bundles `backwards/` (Concourse YAML compat).                                                                                                          |                                                                                                                          |
| `node` (LTS) + `npm`                   | `docs/`, `e2e/`, `server/static/`, `examples/`, `packages/pocketci/` each have their own `package.json` and `package-lock.json`. Always run `npm install` from inside the relevant subdir before its npm scripts. |                                                                                                                          |
| `shellcheck`, `shfmt`                  | Lint/format `bin/*.sh`                                                                                                                                                                                            |                                                                                                                          |
| `golangci-lint` (v2 config)            | `.golangci.yml`. Skipped locally when `CI=true` to avoid double work — CI runs it as its own job.                                                                                                                 |                                                                                                                          |
| `playwright` (`@playwright/test` 1.59) | E2E. Browsers cached in CI under `~/.cache/ms-playwright`. Install with `cd e2e && npx playwright install --with-deps` once.                                                                                      |                                                                                                                          |
| **Docker**                             | Required for almost every test path that exercises the Docker driver, runtime, examples, or e2e. The `task cleanup` target wipes leaked Docker containers/volumes — `task test` invokes it before and after.      |                                                                                                                          |

`Brewfile` covers `go-task`, `go`, `deno`, `shellcheck`, `shfmt`, `minio`. There
is no `node` in `Brewfile` — install separately if needed.

## Build / test / lint — canonical commands

The default target `task` runs **build → fmt → test** end to end. This is what
CI ultimately runs (`task` job in `.github/workflows/go.yml`).

```bash
# 1) Bootstrap (one-time per checkout / when package-lock changes)
cd docs           && npm install && cd ..
cd e2e            && npm install && cd ..
cd server/static  && npm install && cd ..
cd examples       && npm install && cd ..   # only if you touch examples
# Playwright browsers (e2e tests only):
cd e2e && npx playwright install --with-deps && cd ..

# 2) Build static assets, docs, and run go generate
task build           # = build:static + build:docs + go generate ./...
task build:binary    # standalone: produces ./pocketci with -ldflags="-s -w" -trimpath

# 3) Format + lint everything (deno fmt/lint, gofmt, shfmt, shellcheck, golangci-lint --fix)
task fmt
# Note: golangci-lint is SKIPPED inside `task fmt` when CI=true, since CI runs it as its own step.

# 4) Tests
task test            # cleanup → go test -race ./... → cleanup → playwright (subset)
task test:e2e        # only the playwright subset (accordion, docs, pipelines, search_pagination, share)

# 5) Everything (mirrors the CI Task step)
task                 # build + fmt + test
```

Important caveats observed in practice:

- **Always run `task cleanup` (or use `task test`) before/after Docker-driven
  tests**, otherwise leaked containers/volumes from a previous failed run can
  break the next run. `bin/cleanup.sh` force-removes ALL local containers and
  volumes — do not run it on a workstation that has unrelated Docker work.
- **`task build:static` and `task build:docs` MUST run before
  `go generate ./...`** because the Go embed targets pull in
  `server/static/dist/` and `server/docs/site/`. The default `task build`
  already orders them correctly — prefer it over running steps individually.
- `task fmt` will mutate files (`-w`, `--fix`). If you only want to verify, use
  `gofmt -l .`, `deno fmt --check`, `golangci-lint run ./...` (no `--fix`).
- Tests use `-race`. Some tests rely on `goleak` (see `*_goleak_test.go` files
  in `runtime/`, `server/`, `storage/`, `orchestra/`) — leaked goroutines fail
  the package.
- The `go.yml` workflow has `timeout-minutes: 15`. Plan tests accordingly; a
  full local `task` typically takes several minutes plus Docker time.
- **Do not commit** `*.db`, `*.test`, `pocketci`/`pocketci.exe` binaries, or
  anything in `.scratch/` — they're in `.gitignore`.
- **Do not commit** `.envrc`, `.env`, `.dev.vars*` — they may carry secrets.
- The Kubernetes driver tests require `minikube` (CI uses
  `medyagh/setup-minikube`). Locally these tests are skipped if
  minikube/kubeconfig is unavailable.
- The macOS VZ driver test has its own task: `task test:vz` (codesigns the test
  binary with a virtualization entitlement). Don't run on Linux.

## CI pipeline (must pass)

`.github/workflows/go.yml` on every push/PR to `main`:

1. Checkout, set up `shfmt`, Node (with npm caches for `docs/`, `e2e/`,
   `server/static/`), Deno v2.x, **minikube** (with
   `PodLogsQuerySplitStreams=true`), Playwright browsers, Go `stable`, Task.
2. `task build:static`
3. `task build:docs`
4. `golangci-lint` (via `golangci/golangci-lint-action@v8`, reads
   `.golangci.yml`)
5. `task` (= `build` + `fmt` + `test`)

`.github/workflows/release.yml` runs on `v*` tags via GoReleaser
(`.goreleaser.yml`) — ignore for normal PRs.

Dependabot is configured (`.github/dependabot.yml`).

## Project layout — where things live

Top-level structure:

```
.
├── main.go                   # CLI entry; defines kong CLI struct, slog/tint logging
├── commands/                 # Cobra-style subcommands wired into main.go: execute, pipeline, resource, server, login, schedule, etc.
├── runtime/                  # JS/TS pipeline runtime (goja). jsapi/ exposes runtime.run/agent/notify; backwards/ runs Concourse YAML
│   ├── agent/                # In-pipeline LLM agent runtime + tools
│   ├── backwards/            # Bundled (deno/esbuild) Concourse YAML compatibility layer
│   ├── jsapi/                # Goja bindings (runtime.run, runtime.cache, runtime.secret, runtime.notify, …)
│   ├── runner/               # Pipeline runner & lifecycle
│   └── support/              # Shared helpers
├── orchestra/                # Pluggable execution drivers
│   ├── docker/ k8s/ fly/ digitalocean/ hetzner/ qemu/ vz/ native/
│   ├── driver_registry.go    # Driver registration table
│   └── orchestrator.go       # Orchestrator interface contract
├── server/                   # HTTP server: REST API, HTMX UI, MCP server, webhook routes
│   ├── api_*_controller.go   # JSON API controllers (runs, pipelines, schedules, gates, drivers, features, share, webhooks)
│   ├── web_*_controller.go   # HTMX/HTML controllers
│   ├── templates/            # Server-rendered HTML templates (excluded from deno fmt)
│   ├── static/               # Tailwind v4 + esbuild front-end source (`src/`) and build output (`dist/`)
│   ├── docs/                 # VitePress build output, embedded into the binary
│   ├── auth/                 # OAuth + basic auth
│   └── mcp_server.go         # Model Context Protocol server
├── storage/                  # SQLite persistence layer (drivers, FTS, tree, runs, agent memory)
│   └── sqlite/
├── cache/                    # Build cache abstraction (filesystem + S3 backends, zstd compression)
├── secrets/                  # Secret stores: noop, sqlite, s3 (with encryption)
├── webhooks/                 # Provider receivers: github, gitlab, bitbucket, slack, sentry, stripe, linear, pagerduty, honeybadger, generic, filter
├── resources/                # Native resource implementations + registry
├── scheduler/                # Cron-style schedule executor
├── executor/                 # Process/exec helpers
├── observability/            # Metrics, tracing, slog setup
├── client/                   # Go client SDK for the PocketCI API
├── e2e/                      # Playwright tests (TypeScript)
├── examples/                 # Real pipelines (run as part of the test suite via examples_test.go)
├── docs/                     # VitePress source (Markdown). Built into server/docs/site/ at build time
├── benchmarks/               # k6 API load tests + helper scripts
├── bin/                      # cleanup.sh, k6-with-server.sh
├── packages/pocketci/        # Public TS package (npm) for pipeline authors
├── backwards/                # Concourse-YAML compat bundle source (built by deno into runtime/backwards/)
├── testhelpers/              # Shared Go test helpers
├── Taskfile.yml              # SOURCE OF TRUTH for build/test/lint — read this first
├── .golangci.yml             # Linter config (cyclop=15, gocognit=30, nestif=7, …)
├── .goreleaser.yml           # Release config (goos: linux/darwin/windows; goarch: amd64/arm64)
├── Dockerfile.fly            # Multi-stage Docker build for Fly.io
├── docker-entrypoint.sh      # Maps Tigris env vars → CI_CACHE_S3_* at container start
├── fly.toml                  # Fly.io app config
├── pocketci.rb               # Homebrew formula stub (used by goreleaser brew tap)
```

### Configuration / linting files at a glance

- `Taskfile.yml` — every build/test/lint command goes through here.
- `.golangci.yml` — Go linters (`errcheck`, `govet`, `staticcheck`, `wrapcheck`,
  `errorlint`, `musttag`, `cyclop`, `gocognit`, `nestif`, `sloglint`,
  `perfsprint`, `prealloc`, `containedctx`, `contextcheck`, more). `musttag` is
  load-bearing because Goja relies on JSON tags.
- `.gitignore` — already excludes `*.db`, `*.test`, `node_modules/`,
  `server/docs/site/`, `site/docs/`, `backwards/bundle.js`, `.scratch/`, and
  generated assets.
- `deno.json`/`deno.jsonc` is **not** present at the root — Deno is invoked with
  explicit ignore lists in `Taskfile.yml`'s `fmt` task. If you add TS files,
  ensure they pass `deno fmt`/`deno lint` (or are added to the ignore list
  there).
- `.envrc` is gitignored. It is a developer-local file; **never commit it** and
  never read it in CI.

### Code conventions worth knowing

- All loggers come through `log/slog` configured in `main.go` (text via
  [`tint`](https://github.com/lmittmann/tint), or JSON when
  `--log-format=json`). `sloglint` enforces consistency. Use
  `slog.DiscardHandler`, not `slog.NewTextHandler(io.Discard, nil)`.
- Errors crossing package boundaries should be wrapped (`%w`); `wrapcheck` and
  `errorlint` will flag violations.
- Anything that the Goja JS VM marshals must have JSON struct tags (`musttag`).
- Don't store `context.Context` inside structs (`containedctx`); always
  propagate it as the first argument (`contextcheck`).
- Receiver style must be consistent per type (`recvcheck`).
- If you hit cyclop/gocognit/nestif limits, refactor — don't `//nolint` unless
  the directive is precise (`nolintlint` checks staleness).

### Testing tips

- `go test -race ./...` runs everything. To target a package:
  `go test -race
  ./server/... -run TestX`. Most tests are deterministic; flaky
  tests usually indicate a real race or leaked goroutine.
- `examples/examples_test.go` actually executes the pipelines under `examples/`
  against Docker. They will fail without Docker available.
- Playwright tests in `e2e/tests/` run against a freshly created `e2e-test.db`;
  deleting that DB between runs is part of `task test:e2e`.
- For a UI-only iteration loop: `task server` runs `wgo` (file watcher) →
  `go generate ./...` → `go run main.go server --storage sqlite://test.db`.

### Common pitfalls

- Skipping `task build:static` / `task build:docs` before `go test` causes
  embed-related compile failures because `server/static/dist/bundle.js` and
  `server/docs/site/` are required by `//go:embed` directives.
- Running `golangci-lint` locally with `CI=true` set will be a no-op inside
  `task fmt` (by design) — invoke `golangci-lint run ./...` directly if you want
  to lint without `task`.
- Editing `server/templates/*.html` does not require a rebuild for
  `task
  server`'s watcher (`-file .html` is watched), but a fresh `go run`
  rebuild is needed if you change a `//go:embed` set.
- Do not run `bin/cleanup.sh` manually if you have unrelated Docker workloads —
  it removes **all** containers and volumes.
- The Concourse YAML compatibility bundle lives in `backwards/bundle.js` and is
  generated; do not hand-edit. Source is in `backwards/src/`.

If anything above conflicts with what you observe in the repo, prefer the repo
state, fix the discrepancy in this file, and continue.
