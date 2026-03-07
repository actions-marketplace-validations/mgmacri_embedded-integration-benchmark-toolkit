#!/bin/bash
# run_wal_benchmark.sh — Orchestrates all SQLite WAL benchmark scenarios
#
# Usage: ./run_wal_benchmark.sh [duration_sec]
# Default duration: 60 seconds per scenario
#
# Customize:
#   - Add/remove scenarios to match your expected throughput
#   - Adjust reader counts to match your application's read pattern
#   - Change intervals to simulate your actual write frequencies
set -e

DURATION="${1:-60}"
RESULTS_DIR="results/wal_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$RESULTS_DIR"
DB_DIR="/tmp/wal_benchmark"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SQLITE3="${SCRIPT_DIR}/sqlite3"

# Use bundled sqlite3 CLI, fall back to system
if [ ! -x "$SQLITE3" ]; then
    SQLITE3="$(command -v sqlite3 2>/dev/null || true)"
    if [ -z "$SQLITE3" ]; then
        echo "ERROR: sqlite3 CLI not found. Deploy the sqlite3 binary to $SCRIPT_DIR/" >&2
        exit 1
    fi
fi

echo "SQLite WAL Benchmark — Duration: ${DURATION}s per scenario"
echo "Results: $RESULTS_DIR"

# Collect system info
{
    uname -a
    echo "---"
    cat /proc/meminfo 2>/dev/null || true
    echo "---"
    free -m 2>/dev/null || true
} > "$RESULTS_DIR/system_info.txt"

run_scenario() {
    local SCENARIO_NAME="$1"
    local JOURNAL_MODE="$2"       # "wal" or "delete"
    local C_WRITE_INTERVAL="$3"   # ms between C writer inserts
    local GO_READERS="$4"         # number of concurrent Go readers
    local GO_READ_INTERVAL="$5"   # ms between reads
    local GO_WRITE_INTERVAL="$6"  # 0 = no Go writer

    local SCENARIO_DIR="$RESULTS_DIR/$SCENARIO_NAME"
    mkdir -p "$SCENARIO_DIR"

    echo ""
    echo "=== Scenario: $SCENARIO_NAME ==="
    echo "    journal=$JOURNAL_MODE c_interval=${C_WRITE_INTERVAL}ms readers=$GO_READERS"

    # Fresh database for each scenario
    rm -f "$DB_DIR/test.db" "$DB_DIR/test.db-wal" "$DB_DIR/test.db-shm" "$DB_DIR/test.db-journal"
    mkdir -p "$DB_DIR"

    # Set journal mode FIRST (before creating tables)
    if [ "$JOURNAL_MODE" = "wal" ]; then
        $SQLITE3 "$DB_DIR/test.db" "PRAGMA journal_mode=WAL;"
    else
        $SQLITE3 "$DB_DIR/test.db" "PRAGMA journal_mode=DELETE;"
    fi

    # Create tables
    $SQLITE3 "$DB_DIR/test.db" < schema.sql

    # Start WAL monitor (samples WAL file size every second)
    ./wal_monitor.sh "$DB_DIR/test.db" "$DURATION" > "$SCENARIO_DIR/wal_size.csv" &
    WAL_MON_PID=$!

    # Start C data writer
    ./c_writer "$DB_DIR/test.db" "$C_WRITE_INTERVAL" "$DURATION" > "$SCENARIO_DIR/c_writer.json" &
    C_PID=$!

    # Start Go reader(s) if requested
    GR_PID=""
    if [ "$GO_READERS" -gt 0 ]; then
        ./go_reader "$DB_DIR/test.db" "$GO_READERS" "$GO_READ_INTERVAL" "$DURATION" > "$SCENARIO_DIR/go_reader.json" &
        GR_PID=$!
    fi

    # Start Go writer if interval > 0
    GW_PID=""
    if [ "$GO_WRITE_INTERVAL" -gt 0 ]; then
        ./go_writer "$DB_DIR/test.db" "$GO_WRITE_INTERVAL" "$DURATION" > "$SCENARIO_DIR/go_writer.json" &
        GW_PID=$!
    fi

    # Wait for all processes
    wait $C_PID || true
    [ -n "$GR_PID" ] && wait $GR_PID || true
    [ -n "$GW_PID" ] && wait $GW_PID || true
    kill $WAL_MON_PID 2>/dev/null || true
    wait $WAL_MON_PID 2>/dev/null || true

    # Record final DB file sizes
    ls -la "$DB_DIR/test.db"* > "$SCENARIO_DIR/db_sizes.txt" 2>/dev/null || true

    echo "    Completed: $SCENARIO_NAME"
}

# ============================================================
# ROLLBACK MODE SCENARIOS (baseline)
# ============================================================

run_scenario "rollback_33wps_1reader"   "delete" 30  1 100 0
run_scenario "rollback_33wps_3readers"  "delete" 30  3 100 0
run_scenario "rollback_33wps_rw"        "delete" 30  1 100 500

# ============================================================
# WAL MODE SCENARIOS
# ============================================================

# Baseline (33 writes/sec)
run_scenario "wal_33wps_1reader"        "wal" 30  1 100 0
run_scenario "wal_33wps_3readers"       "wal" 30  3 100 0
run_scenario "wal_33wps_rw"             "wal" 30  1 100 500

# 3x baseline (100 writes/sec)
run_scenario "wal_100wps_1reader"       "wal" 10  1 100 0
run_scenario "wal_100wps_3readers"      "wal" 10  3 100 0
run_scenario "wal_100wps_rw"            "wal" 10  1 100 500

# 10x baseline (330 writes/sec) — stress test
run_scenario "wal_330wps_1reader"       "wal" 3   1 100 0
run_scenario "wal_330wps_3readers"      "wal" 3   3 100 0
run_scenario "wal_330wps_rw"            "wal" 3   1 100 500

# Sustained write + WAL growth test (10 minutes)
run_scenario "wal_33wps_sustained_10m"  "wal" 30  1 100 0

echo ""
echo "All scenarios complete. Results in $RESULTS_DIR"
