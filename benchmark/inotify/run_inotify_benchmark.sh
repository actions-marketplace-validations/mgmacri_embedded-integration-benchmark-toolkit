#!/bin/bash
# run_inotify_benchmark.sh — Orchestrates inotify sentinel benchmark scenarios
#
# Usage: ./run_inotify_benchmark.sh [duration_sec]
# Default duration: 60 seconds per scenario
#
# Customize:
#   - Add scenarios with different intervals to match your config change rate
#   - Adjust the sleep after watcher launch if initialization takes longer
set -e

DURATION="${1:-60}"
RESULTS_DIR="results/inotify_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$RESULTS_DIR"
WATCH_DIR="/tmp/sentinel_bench"

echo "inotify Sentinel Benchmark — Duration: ${DURATION}s per scenario"
echo "Results: $RESULTS_DIR"

run_scenario() {
    local SCENARIO_NAME="$1"
    local INTERVAL_MS="$2"

    echo ""
    echo "=== Scenario: $SCENARIO_NAME (interval=${INTERVAL_MS}ms) ==="

    # Fresh watch directory
    rm -rf "$WATCH_DIR" 2>/dev/null || true
    mkdir -p "$WATCH_DIR"

    # Start watcher (blocks on inotify read)
    ./inotify_watcher "$WATCH_DIR" > "$RESULTS_DIR/watcher_${SCENARIO_NAME}.json" &
    WATCHER_PID=$!
    sleep 1  # Let watcher initialize

    # Start writer
    ./sentinel_writer "$WATCH_DIR" "$INTERVAL_MS" "$DURATION" > "$RESULTS_DIR/writer_${SCENARIO_NAME}.json"
    WRITER_EXIT=$?

    # Stop watcher
    sleep 1  # Let watcher process remaining events
    kill $WATCHER_PID 2>/dev/null || true
    wait $WATCHER_PID 2>/dev/null || true

    echo "    Completed: $SCENARIO_NAME (writer exit=$WRITER_EXIT)"
}

# Scenario 1: Config changes every 500ms (realistic)
run_scenario "500ms" 500

# Scenario 2: Config changes every 100ms (aggressive)
run_scenario "100ms" 100

# Scenario 3: Burst — 1ms interval (stress test)
run_scenario "1ms_burst" 1

# Cleanup
rm -rf "$WATCH_DIR" || true

echo ""
echo "inotify benchmark complete. Results in $RESULTS_DIR"
