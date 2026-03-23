#!/usr/bin/env bash
set -e

# Start server in background
# If PPROF=1, enable the pprof debug endpoint on :6060
PPROF_FLAG=""
if [ "${PPROF:-}" = "1" ]; then
	PPROF_FLAG="--pprof-addr=:6060"
fi

# shellcheck disable=SC2086
go run main.go server --storage sqlite://:memory: ${PPROF_FLAG} &
SERVER_PID=$!

# Cleanup function to ensure server is killed
cleanup() {
	kill "$SERVER_PID" 2>/dev/null || true
}
trap cleanup EXIT

# Wait for server to be ready (max 15 seconds)
echo "Waiting for server to start..."
for _ in $(seq 1 30); do
	if curl -s http://localhost:8080/health >/dev/null 2>&1; then
		echo "Server is ready"
		break
	fi
	sleep 0.5
done

# Check if server started successfully
if ! curl -s http://localhost:8080/health >/dev/null 2>&1; then
	echo "Error: Server failed to start within 15 seconds"
	exit 1
fi

# Run k6 with all provided arguments
k6 "$@"
