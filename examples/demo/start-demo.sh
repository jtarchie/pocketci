#!/usr/bin/env bash

set -eux

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
DEMO_DB="$SCRIPT_DIR/demo.db"
PORT=8080
SERVER_URL="http://localhost:$PORT"
DRIVER="docker"

cleanup() {
    lsof -ti:"$PORT" | xargs kill -9 2>/dev/null || true
}

trap cleanup EXIT INT TERM

# Kill any existing process on the port and reset DB
cleanup
rm -f "$DEMO_DB"

cd "$PROJECT_ROOT"
go run . server --storage "sqlite://$DEMO_DB" --port "$PORT" 2>&1 &
SERVER_PID=$!

# Wait for server to be healthy
for _ in $(seq 1 30); do
    curl -s "$SERVER_URL/health" >/dev/null 2>&1 && break
    kill -0 "$SERVER_PID" 2>/dev/null || { echo "server exited unexpectedly" >&2; exit 1; }
    sleep 1
done

# Upload all pipelines
for yaml_file in "$SCRIPT_DIR"/*.yml; do
    [ -f "$yaml_file" ] || continue
    go run . pipeline set --server-url "$SERVER_URL" --name "$(basename "$yaml_file" .yml)" --driver "$DRIVER" "$yaml_file"
done

wait "$SERVER_PID"
