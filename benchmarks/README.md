# Benchmarks

This directory contains load testing and benchmarking scripts for the PocketCI
system.

## Go Benchmarks

Run micro-benchmarks for transpilation and storage:

```bash
task bench
```

This runs:

- `runtime/js_bench_test.go` - TypeScript transpilation benchmarks
- `storage/driver_bench_test.go` - SQLite storage CRUD benchmarks
- `examples/bench_test.go` - Full pipeline execution benchmarks

## k6 API Load Tests

The `api_load.js` script tests the HTTP API under load.

### Prerequisites

Install k6:

```bash
brew install k6
```

### Running Load Tests

1. Start the CI server:
   ```bash
   go run main.go server --storage-sqlite-path bench.db
   ```

2. Run the load test:
   ```bash
   # Quick smoke test
   k6 run --vus 1 --duration 10s benchmarks/api_load.js

   # Full scenario test (smoke + load + stress)
   k6 run benchmarks/api_load.js

   # Custom parameters
   k6 run --vus 20 --duration 60s benchmarks/api_load.js

   # Against different host
   BASE_URL=http://ci-server:8080 k6 run benchmarks/api_load.js
   ```

### What's Tested

- **Pipeline CRUD**: Create, list, get, delete pipelines
- **Pipeline Execution**: Trigger pipelines, poll run status
- **Metrics tracked**:
  - `pipeline_create_duration` - Time to create a pipeline
  - `pipeline_trigger_duration` - Time to trigger execution
  - `pipeline_list_duration` - Time to list all pipelines
  - `run_status_duration` - Time to check run status
  - `error_rate` - Percentage of failed requests

### Thresholds

The test will fail if:

- 95th percentile response time > 2s
- Error rate > 10%
- Pipeline create time > 1s at p95
- Pipeline trigger time > 500ms at p95

## Pipeline Execution Benchmarks

Located in `examples/bench_test.go`, these test full pipeline execution with
Docker:

- `BenchmarkPipeline_HelloWorld` - Standard hello world pipeline
- `BenchmarkPipeline_Promises` - Parallel task execution
- `BenchmarkPipeline_Minimal` - Minimal container startup overhead
- `BenchmarkPipeline_Volumes` - Volume mount operations

## Comparing Benchmark Results

Use `benchstat` to compare benchmark runs:

```bash
# Install benchstat
go install golang.org/x/perf/cmd/benchstat@latest

# Run benchmarks and save baseline
go test -bench=. -count=5 ./... > baseline.txt

# Make changes, then compare
go test -bench=. -count=5 ./... > new.txt
benchstat baseline.txt new.txt
```
