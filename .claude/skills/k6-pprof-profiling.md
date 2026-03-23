# Skill: k6 Load Test + pprof Profiling

Profile the pocketci server under realistic API load and analyse CPU, heap,
allocation and goroutine hotspots.

---

## Prerequisites

All tools must be installed (`brew bundle`):

- `go` 1.25+
- `k6` (Grafana k6 load-testing tool)
- `go tool pprof` (ships with Go)

The server must be built with the pprof endpoint enabled. No extra build tags
are needed — `net/http/pprof` is wired in via `server/pprof.go` and activated at
runtime with `--pprof-addr`.

---

## Quick path (via `task`)

```bash
task bench:profile
```

This starts the server with `PPROF=1` (enabling `--pprof-addr=:6060`), runs k6
for 30 s, then opens an **interactive flame graph** at `http://localhost:8888`.

> Note: `task bench:profile` opens a browser — it blocks the terminal and is
> intended for interactive use. Use the manual path below for headless / CI
> profiling.

---

## Manual step-by-step

### 1. Build the binary

```bash
go build -o /tmp/pocketci-server .
```

### 2. Start the server with pprof

```bash
/tmp/pocketci-server server \
  --storage-sqlite-path=/tmp/pprof-bench.db \
  --pprof-addr=:6060 \
  --port=8082 \
  --secrets-sqlite-passphrase=testing \
  > /tmp/server.log 2>&1 &
echo "server pid=$!"
```

Wait for it to be ready:

```bash
sleep 2 && grep 'server started' /tmp/server.log
```

Default pprof port is `:6060`. Default server port is `:8082` (use a non-8080
port to avoid conflicts with other local services).

### 3. Run k6 load

```bash
export BASE_URL=http://localhost:8082
k6 run --vus 10 --duration 35s benchmarks/api_load.js
```

Typical clean-run output (all checks green):

```
✓ create status is 200          ← PUT /api/pipelines/:name
✓ create returns pipeline id
✓ list status is 200
✓ list returns items array      ← response shape: { items: [...], page, ... }
✓ get status is 200
✓ trigger status is 202 or 429  ← 429 = MaxInFlight reached (expected)
✓ trigger returns run id or rate-limited
✓ status request succeeds
✓ delete status is 204
error_rate: 0.00%
```

Expected throughput at 10 VUs on a local dev machine: ~470 req/s.

#### Known API shape (benchmarks/api_load.js)

| Operation              | Method   | Path                              | Success status                                                                |
| ---------------------- | -------- | --------------------------------- | ----------------------------------------------------------------------------- |
| Create/update pipeline | `PUT`    | `/api/pipelines/:name`            | 200                                                                           |
| List pipelines         | `GET`    | `/api/pipelines`                  | 200 — body is `{ items, page, per_page, total_items, total_pages, has_next }` |
| Get pipeline           | `GET`    | `/api/pipelines/:id`              | 200                                                                           |
| Trigger pipeline       | `POST`   | `/api/pipelines/:id/trigger`      | 202 (or 429 when `MaxInFlight` is full)                                       |
| Poll run status        | `GET`    | `/api/pipelines/:id/runs/:run_id` | 200 or 404                                                                    |
| Delete pipeline        | `DELETE` | `/api/pipelines/:id`              | 204                                                                           |

> `POST /api/pipelines` is **not** a valid route — creation uses `PUT`.

### 4. Collect profiles (while k6 is running or after)

Run k6 in the background, then collect profiles while load is live:

```bash
export BASE_URL=http://localhost:8082
k6 run --vus 10 --duration 35s benchmarks/api_load.js > /tmp/k6.log 2>&1 &

# CPU profile (25 s sample — start this while k6 is mid-run)
sleep 3
go tool pprof -text -nodecount=30 \
  'http://localhost:6060/debug/pprof/profile?seconds=25' \
  > /tmp/cpu.pprof.txt

# Heap (in-use allocations)
go tool pprof -text -nodecount=20 \
  'http://localhost:6060/debug/pprof/heap' \
  > /tmp/heap.txt

# All allocations (since process start)
go tool pprof -text -nodecount=20 \
  'http://localhost:6060/debug/pprof/allocs' \
  > /tmp/allocs.txt

# Goroutine count / stacks
go tool pprof -text -nodecount=10 \
  'http://localhost:6060/debug/pprof/goroutine' \
  > /tmp/goroutine.txt

wait  # wait for k6 to finish
cat /tmp/k6.log
```

For an **interactive flame graph** of the CPU profile:

```bash
go tool pprof -http=:8888 \
  'http://localhost:6060/debug/pprof/profile?seconds=25'
```

### 5. Stop the server

```bash
pkill -f pocketci-server
```

---

## Interpreting results

### CPU profile

The server is I/O-bound. Most CPU time is in `syscall.rawsyscalln` (I/O) and
`runtime.pthread_cond_wait` (goroutines blocking). Expected at moderate (<50 VU)
load:

| Symbol                                    | Meaning                              |
| ----------------------------------------- | ------------------------------------ |
| `syscall.rawsyscalln` ~50%                | Raw I/O syscalls — normal, I/O-bound |
| `pthread_cond_wait` ~11%                  | Goroutines parked waiting for work   |
| `modernc.org/sqlite/lib._sqlite3VdbeExec` | SQLite VM execution                  |
| `modernc.org/libc._lockBtree`             | SQLite B-tree locking                |
| `tint.(*buffer).WriteString`              | Structured log formatting            |

### Heap / alloc profile

Key hotspots to watch:

| Symbol                               | Cause                                                              | Actionable?                                                                    |
| ------------------------------------ | ------------------------------------------------------------------ | ------------------------------------------------------------------------------ |
| `net/http.Header.Clone`              | Every HTTP request clones headers                                  | No — stdlib overhead                                                           |
| `modernc.org/sqlite.interruptOnDone` | Pure-Go SQLite spawns goroutine+channel per query for cancellation | Yes — watch at high concurrency; consider `mattn/go-sqlite3` (CGO) if it grows |
| `server.newSlogMiddleware`           | Per-request log entry                                              | Use `--log-level=warn` in production                                           |
| `net/textproto.readMIMEHeader`       | HTTP header parsing                                                | No — stdlib overhead                                                           |

### Goroutine profile

Healthy idle state: ~17 goroutines (15 parked). If this grows unboundedly under
load, check for goroutine leaks (see `goleak` tests).

### Trigger 429s

`POST /api/pipelines/:id/trigger` returns 429 when the executor's `MaxInFlight`
limit is reached (default: 10, configurable via `--max-in-flight` /
`CI_MAX_IN_FLIGHT`). At 10 VUs hammering triggers, Docker container start-up
latency fills the queue. This is **by design**, not an error. The k6 script
correctly treats 429 as non-error.

---

## Tuning options

| Flag              | Default    | Purpose                                         |
| ----------------- | ---------- | ----------------------------------------------- |
| `--pprof-addr`    | (disabled) | Address for the pprof debug HTTP server         |
| `--port`          | 8080       | Main HTTP server port                           |
| `--max-in-flight` | 10         | Max concurrent pipeline executions              |
| `--log-level`     | info       | Reduce to `warn` to cut log-allocation overhead |

To run with higher concurrency limits for stress-testing the queue:

```bash
/tmp/pocketci-server server \
  --storage-sqlite-path=/tmp/bench-stress.db \
  --pprof-addr=:6060 \
  --port=8082 \
  --max-in-flight=50 \
  --secrets-sqlite-passphrase=testing \
  > /tmp/server-stress.log 2>&1 &
```
