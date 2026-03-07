#!/bin/bash
# run_ipc_benchmark.sh — Orchestrates IPC socket comparison benchmark
#
# Runs 3 scenarios matching inotify benchmark intervals (500ms, 100ms, 1ms burst)
# for apples-to-apples comparison.
#
# Customize:
#   - Add scenarios with different intervals
#   - Change socket path if /tmp is not suitable
set -e

RESULTS_DIR="results/ipc_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$RESULTS_DIR"
SOCKET_PATH="/tmp/bench_ipc.sock"
DURATION="${1:-60}"

echo "IPC Socket Benchmark — Duration: ${DURATION}s per scenario"
echo "Results: $RESULTS_DIR"

run_scenario() {
    local SCENARIO_NAME="$1"
    local INTERVAL_MS="$2"

    echo ""
    echo "=== Scenario: $SCENARIO_NAME (interval=${INTERVAL_MS}ms) ==="

    # Clean up stale socket
    rm -f "$SOCKET_PATH"

    # Start C server
    ./ipc_server "$SOCKET_PATH" > "$RESULTS_DIR/server_${SCENARIO_NAME}.json" &
    SERVER_PID=$!
    sleep 1  # Let server bind

    # Start Go client
    ./ipc_client "$SOCKET_PATH" "$INTERVAL_MS" "$DURATION" > "$RESULTS_DIR/client_${SCENARIO_NAME}.json"
    CLIENT_EXIT=$?

    # Server exits when client disconnects; wait for it
    wait $SERVER_PID 2>/dev/null || true

    echo "    Completed: $SCENARIO_NAME (client exit=$CLIENT_EXIT)"
}

# Scenario 1: Config changes every 500ms (realistic)
run_scenario "500ms" 500

# Scenario 2: Config changes every 100ms (aggressive)
run_scenario "100ms" 100

# Scenario 3: Burst — 1ms interval (stress test)
run_scenario "1ms_burst" 1

# Cleanup
rm -f "$SOCKET_PATH"

echo ""
echo "IPC benchmark complete. Results in $RESULTS_DIR"
