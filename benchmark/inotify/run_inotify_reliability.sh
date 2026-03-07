#!/bin/bash
# run_inotify_reliability.sh — Tests inotify failure modes and degradation
#
# Runs 5 reliability scenarios that stress-test inotify's edge cases:
#   1. Queue overflow       — Flood events to trigger IN_Q_OVERFLOW
#   2. I/O storm            — Concurrent dd during benchmark
#   3. Rapid overwrites     — burst-pairs=5 to test event coalescing
#   4. fsync race           — Writer skips fsync, reader delays file read
#   5. tmpfs vs block       — Compare /tmp (tmpfs) vs /var/tmp (block storage)
#
# Usage: ./run_inotify_reliability.sh [duration_sec]
# Default duration: 30 seconds per scenario (shorter since stress tests)
#
# IMPORTANT: These tests are designed to reveal failure modes. Non-zero
# overflow_events, coalesced_events, or missed_events are EXPECTED in some
# scenarios. The report compares these counters to the clean baseline.
set -e

DURATION="${1:-30}"
RESULTS_DIR="results/inotify_reliability_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$RESULTS_DIR"

WATCH_DIR="/tmp/sentinel_bench_rel"
IO_STORM_PID=""
WATCHER_PID=""

cleanup() {
    if [ -n "$IO_STORM_PID" ]; then
        kill "$IO_STORM_PID" 2>/dev/null || true
        wait "$IO_STORM_PID" 2>/dev/null || true
    fi
    if [ -n "$WATCHER_PID" ]; then
        kill "$WATCHER_PID" 2>/dev/null || true
        wait "$WATCHER_PID" 2>/dev/null || true
    fi
    rm -rf "$WATCH_DIR" /tmp/io_storm_bench.* 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "inotify Reliability Tests — Duration: ${DURATION}s per scenario"
echo "Results: $RESULTS_DIR"

# ---------- Scenario 1: Queue overflow ----------
# Uses 1ms burst interval to overwhelm the inotify queue.
# The watcher adds a --delay-start-ms to slow down consumption so events pile up.
run_overflow() {
    echo ""
    echo "=== Scenario: queue_overflow ==="

    rm -rf "$WATCH_DIR" 2>/dev/null || true
    mkdir -p "$WATCH_DIR"

    # Slow reader: 50ms delay before reading each file
    ./inotify_watcher "$WATCH_DIR" --delay-start-ms 50 \
        > "$RESULTS_DIR/watcher_queue_overflow.json" &
    WATCHER_PID=$!
    sleep 1

    # Fast writer: 1ms interval = 1000 events/sec
    ./sentinel_writer "$WATCH_DIR" 1 "$DURATION" \
        > "$RESULTS_DIR/writer_queue_overflow.json"

    sleep 1
    kill $WATCHER_PID 2>/dev/null || true
    wait $WATCHER_PID 2>/dev/null || true
    WATCHER_PID=""

    echo "    Completed: queue_overflow"
}

# ---------- Scenario 2: I/O storm ----------
# Run concurrent dd writes to saturate the storage I/O path while benchmark runs.
run_io_storm() {
    echo ""
    echo "=== Scenario: io_storm ==="

    rm -rf "$WATCH_DIR" 2>/dev/null || true
    mkdir -p "$WATCH_DIR"

    ./inotify_watcher "$WATCH_DIR" \
        > "$RESULTS_DIR/watcher_io_storm.json" &
    WATCHER_PID=$!
    sleep 1

    # Start I/O storm in background: continuous 1MB writes
    (
        while true; do
            dd if=/dev/urandom of=/tmp/io_storm_bench.dat bs=1M count=1 \
                conv=fdatasync 2>/dev/null
        done
    ) &
    IO_STORM_PID=$!

    ./sentinel_writer "$WATCH_DIR" 100 "$DURATION" \
        > "$RESULTS_DIR/writer_io_storm.json"

    # Stop I/O storm
    kill $IO_STORM_PID 2>/dev/null || true
    wait $IO_STORM_PID 2>/dev/null || true
    IO_STORM_PID=""
    rm -f /tmp/io_storm_bench.dat

    sleep 1
    kill $WATCHER_PID 2>/dev/null || true
    wait $WATCHER_PID 2>/dev/null || true
    WATCHER_PID=""

    echo "    Completed: io_storm"
}

# ---------- Scenario 3: Rapid overwrites (event coalescing) ----------
# Writer sends 5 rapid overwrites to the same filename per tick.
# inotify may coalesce these into a single event.
run_rapid_overwrite() {
    echo ""
    echo "=== Scenario: rapid_overwrite ==="

    rm -rf "$WATCH_DIR" 2>/dev/null || true
    mkdir -p "$WATCH_DIR"

    ./inotify_watcher "$WATCH_DIR" \
        > "$RESULTS_DIR/watcher_rapid_overwrite.json" &
    WATCHER_PID=$!
    sleep 1

    # 5 burst pairs at 100ms interval = 50 writes/sec nominally,
    # but watcher may only see ~10 events/sec
    ./sentinel_writer "$WATCH_DIR" 100 "$DURATION" --burst-pairs 5 \
        > "$RESULTS_DIR/writer_rapid_overwrite.json"

    sleep 1
    kill $WATCHER_PID 2>/dev/null || true
    wait $WATCHER_PID 2>/dev/null || true
    WATCHER_PID=""

    echo "    Completed: rapid_overwrite"
}

# ---------- Scenario 4: fsync race ----------
# Writer skips fsync, reader delays 10ms before reading.
# Tests whether file content is readable after rename without fsync.
run_fsync_race() {
    echo ""
    echo "=== Scenario: fsync_race ==="

    rm -rf "$WATCH_DIR" 2>/dev/null || true
    mkdir -p "$WATCH_DIR"

    # Reader delays 10ms — enough for page cache to maybe not flush
    ./inotify_watcher "$WATCH_DIR" --delay-start-ms 10 \
        > "$RESULTS_DIR/watcher_fsync_race.json" &
    WATCHER_PID=$!
    sleep 1

    # Writer skips fsync before rename
    ./sentinel_writer "$WATCH_DIR" 10 "$DURATION" --no-sync \
        > "$RESULTS_DIR/writer_fsync_race.json"

    sleep 1
    kill $WATCHER_PID 2>/dev/null || true
    wait $WATCHER_PID 2>/dev/null || true
    WATCHER_PID=""

    echo "    Completed: fsync_race"
}

# ---------- Scenario 5: tmpfs vs block filesystem ----------
# Runs baseline on /tmp (usually tmpfs) and /var/tmp (usually ext4/block).
# Compares notification latency between RAM-backed and disk-backed filesystems.
run_tmpfs_vs_block() {
    echo ""
    echo "=== Scenario: tmpfs_baseline (on /tmp) ==="

    local TMPFS_DIR="/tmp/sentinel_bench_tmpfs"
    rm -rf "$TMPFS_DIR" 2>/dev/null || true
    mkdir -p "$TMPFS_DIR"

    ./inotify_watcher "$TMPFS_DIR" \
        > "$RESULTS_DIR/watcher_tmpfs.json" &
    WATCHER_PID=$!
    sleep 1

    ./sentinel_writer "$TMPFS_DIR" 100 "$DURATION" \
        > "$RESULTS_DIR/writer_tmpfs.json"

    sleep 1
    kill $WATCHER_PID 2>/dev/null || true
    wait $WATCHER_PID 2>/dev/null || true
    WATCHER_PID=""
    rm -rf "$TMPFS_DIR" 2>/dev/null || true

    echo "    Completed: tmpfs_baseline"

    echo ""
    echo "=== Scenario: block_baseline (on /var/tmp) ==="

    local BLOCK_DIR="/var/tmp/sentinel_bench_block"
    rm -rf "$BLOCK_DIR" 2>/dev/null || true
    mkdir -p "$BLOCK_DIR"

    ./inotify_watcher "$BLOCK_DIR" \
        > "$RESULTS_DIR/watcher_block.json" &
    WATCHER_PID=$!
    sleep 1

    ./sentinel_writer "$BLOCK_DIR" 100 "$DURATION" \
        > "$RESULTS_DIR/writer_block.json"

    sleep 1
    kill $WATCHER_PID 2>/dev/null || true
    wait $WATCHER_PID 2>/dev/null || true
    WATCHER_PID=""
    rm -rf "$BLOCK_DIR" 2>/dev/null || true

    echo "    Completed: block_baseline"
}

# ---------- Run all scenarios ----------
run_overflow
run_io_storm
run_rapid_overwrite
run_fsync_race
run_tmpfs_vs_block

echo ""
echo "inotify reliability tests complete. Results in $RESULTS_DIR"
