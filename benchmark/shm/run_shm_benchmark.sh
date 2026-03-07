#!/bin/bash
# run_shm_benchmark.sh — Orchestrates shared memory (mmap + FIFO) benchmark
#
# Runs 3 scenarios matching inotify/IPC benchmark intervals (500ms, 100ms,
# 1ms burst) for apples-to-apples comparison across all three transports.
#
# Usage: ./run_shm_benchmark.sh [duration_sec]
# Default duration: 60 seconds per scenario
#
# Cleanup:
#   Removes shared memory segment (/dev/shm/bench_shm) and FIFO
#   (/tmp/bench_shm_fifo) on exit, including on SIGINT/SIGTERM.
set -e

DURATION="${1:-60}"
RESULTS_DIR="results/shm_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$RESULTS_DIR"

READER_PID=""

cleanup() {
    if [ -n "$READER_PID" ]; then
        kill "$READER_PID" 2>/dev/null || true
        wait "$READER_PID" 2>/dev/null || true
    fi
    rm -f /dev/shm/bench_shm /tmp/bench_shm_fifo
}
trap cleanup EXIT INT TERM

echo "Shared Memory (mmap + FIFO) Benchmark — Duration: ${DURATION}s per scenario"
echo "Results: $RESULTS_DIR"

run_scenario() {
    local SCENARIO_NAME="$1"
    local INTERVAL_MS="$2"

    echo ""
    echo "=== Scenario: $SCENARIO_NAME (interval=${INTERVAL_MS}ms) ==="

    # Clean up stale artifacts from previous scenario
    rm -f /dev/shm/bench_shm /tmp/bench_shm_fifo

    # Start C reader (creates shm + FIFO, blocks on FIFO reads)
    ./shm_reader > "$RESULTS_DIR/reader_${SCENARIO_NAME}.json" &
    READER_PID=$!
    sleep 1  # Let reader create shm and FIFO

    # Verify reader is ready
    if [ ! -p /tmp/bench_shm_fifo ]; then
        echo "    ERROR: Reader did not create FIFO"
        kill $READER_PID 2>/dev/null || true
        wait $READER_PID 2>/dev/null || true
        READER_PID=""
        return 1
    fi

    # Start Go writer (opens shm + FIFO, sends config payloads)
    ./shm_writer "$INTERVAL_MS" "$DURATION" > "$RESULTS_DIR/writer_${SCENARIO_NAME}.json"
    WRITER_EXIT=$?

    # Stop reader
    sleep 1  # Let reader process remaining events
    kill $READER_PID 2>/dev/null || true
    wait $READER_PID 2>/dev/null || true
    READER_PID=""

    echo "    Completed: $SCENARIO_NAME (writer exit=$WRITER_EXIT)"
}

# Scenario 1: Config changes every 500ms (realistic)
run_scenario "500ms" 500

# Scenario 2: Config changes every 100ms (aggressive)
run_scenario "100ms" 100

# Scenario 3: Burst — 1ms interval (stress test)
run_scenario "1ms_burst" 1

echo ""
echo "Shared memory benchmark complete. Results in $RESULTS_DIR"
