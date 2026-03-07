#!/bin/bash
# run_all.sh — Complete benchmark suite with stress testing and memory profiling
#
# Runs all benchmarks at full duration, then again under synthetic system load.
# Captures memory profiles at each stage.
#
# Usage: ./run_all.sh [duration_sec]
# Default duration: 60 seconds per scenario
#
# Customize:
#   - Adjust stress parameters (CPU cores, memory allocation) for your hardware
#   - Change sustained test duration cap
#   - Add or remove benchmark phases
set -e

DURATION="${1:-60}"
TIMESTAMP="$(date +%Y%m%d_%H%M%S)"
TOP_RESULTS="results/final_${TIMESTAMP}"
mkdir -p "$TOP_RESULTS"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "=============================================="
echo " Integration Benchmark Suite"
echo " Duration: ${DURATION}s per scenario"
echo " Results:  ${TOP_RESULTS}"
echo " Started:  $(date -u)"
echo "=============================================="

# ------------------------------------------------------------------
# Helper: capture memory snapshot
# ------------------------------------------------------------------
capture_memory() {
    local label="$1"
    local outdir="${TOP_RESULTS}/memory"
    mkdir -p "$outdir"
    {
        echo "=== $label ==="
        date -u
        cat /proc/meminfo
        echo "---"
        ps -eo pid,rss,vsz,comm --sort=-rss 2>/dev/null | head -20 || ps aux | head -20
    } > "$outdir/${label}_meminfo.txt"
    echo "  Memory snapshot: ${label}_meminfo.txt"
}

# ------------------------------------------------------------------
# Helper: pure-shell stress generator (no stress-ng needed)
# Generates CPU, I/O, and memory pressure using shell built-ins.
# Adjust parameters for your target hardware.
# ------------------------------------------------------------------
STRESS_PIDS=""

start_stress() {
    echo ""
    echo ">>> Starting synthetic system stress..."

    # CPU stress: 2 busy loops (one per core) doing sha256
    for i in 1 2; do
        ( while true; do echo "burn" | sha256sum > /dev/null; done ) &
        STRESS_PIDS="$STRESS_PIDS $!"
    done

    # I/O stress: continuous random reads
    ( while true; do dd if=/dev/urandom of=/dev/null bs=64k count=64 2>/dev/null; done ) &
    STRESS_PIDS="$STRESS_PIDS $!"

    # Memory pressure: allocate ~100MB in tmpfs
    # Adjust size based on available RAM on your target
    mkdir -p /dev/shm/stress_bench
    dd if=/dev/urandom of=/dev/shm/stress_bench/blob bs=1M count=100 2>/dev/null &
    STRESS_PIDS="$STRESS_PIDS $!"

    # Wait for memory allocation to finish, keep CPU/IO running
    wait ${STRESS_PIDS##* } 2>/dev/null || true

    echo "  CPU: 2 sha256 loops"
    echo "  I/O: continuous random reads"
    echo "  MEM: 100MB allocated in /dev/shm"
    echo "  PIDs: $STRESS_PIDS"
    sleep 2  # Let stress settle
}

stop_stress() {
    echo ""
    echo ">>> Stopping synthetic stress..."
    for pid in $STRESS_PIDS; do
        kill "$pid" 2>/dev/null || true
    done
    wait 2>/dev/null || true
    rm -rf /dev/shm/stress_bench
    STRESS_PIDS=""
    sleep 2  # Let system settle
    echo "  Stress stopped."
}

# ------------------------------------------------------------------
# Trap: clean up stress on exit
# ------------------------------------------------------------------
cleanup() {
    stop_stress 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# ------------------------------------------------------------------
# Set CPU governor to performance for consistent results (optional)
# ------------------------------------------------------------------
echo ""
echo ">>> Setting CPU governor to performance..."
for c in /sys/devices/system/cpu/cpu[0-9]*; do
    if [ -f "$c/cpufreq/scaling_governor" ]; then
        echo performance > "$c/cpufreq/scaling_governor" 2>/dev/null || true
        cat "$c/cpufreq/scaling_max_freq" > "$c/cpufreq/scaling_min_freq" 2>/dev/null || true
    fi
done
echo "  Done (or not supported on this platform)."

# Collect system info
{
    echo "=== System Info ==="
    uname -a
    echo ""
    cat /proc/cpuinfo 2>/dev/null || true
    echo ""
    free -m 2>/dev/null || true
    echo ""
    df -h /tmp 2>/dev/null || df -h
    echo ""
    echo "CPU governors:"
    cat /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor 2>/dev/null || echo "N/A"
} > "$TOP_RESULTS/system_info.txt"

# ==================================================================
# PHASE 1: Full-duration clean run (no stress)
# ==================================================================
echo ""
echo "======================================================"
echo " PHASE 1: Full-duration benchmarks (clean, ${DURATION}s)"
echo "======================================================"

capture_memory "phase1_before"

echo ""
echo "--- WAL Benchmark (clean) ---"
./run_wal_benchmark.sh "$DURATION"
WAL_CLEAN=$(ls -td results/wal_* | head -1)
mv "$WAL_CLEAN" "$TOP_RESULTS/wal_clean"

echo ""
echo "--- inotify Benchmark (clean) ---"
./run_inotify_benchmark.sh "$DURATION"
INOTIFY_CLEAN=$(ls -td results/inotify_* | head -1)
mv "$INOTIFY_CLEAN" "$TOP_RESULTS/inotify_clean"

echo ""
echo "--- IPC Benchmark (clean) ---"
./run_ipc_benchmark.sh "$DURATION"
IPC_CLEAN=$(ls -td results/ipc_* | head -1)
mv "$IPC_CLEAN" "$TOP_RESULTS/ipc_clean"

echo ""
echo "--- Shared Memory Benchmark (clean) ---"
./run_shm_benchmark.sh "$DURATION"
SHM_CLEAN=$(ls -td results/shm_* | head -1)
mv "$SHM_CLEAN" "$TOP_RESULTS/shm_clean"

capture_memory "phase1_after"

# ==================================================================
# PHASE 2: Full-duration under synthetic stress
# ==================================================================
echo ""
echo "======================================================"
echo " PHASE 2: Full-duration benchmarks (under stress, ${DURATION}s)"
echo "======================================================"

start_stress
capture_memory "phase2_stress_started"

echo ""
echo "--- WAL Benchmark (stressed) ---"
./run_wal_benchmark.sh "$DURATION"
WAL_STRESS=$(ls -td results/wal_* | head -1)
mv "$WAL_STRESS" "$TOP_RESULTS/wal_stress"

echo ""
echo "--- inotify Benchmark (stressed) ---"
./run_inotify_benchmark.sh "$DURATION"
INOTIFY_STRESS=$(ls -td results/inotify_* | head -1)
mv "$INOTIFY_STRESS" "$TOP_RESULTS/inotify_stress"

echo ""
echo "--- IPC Benchmark (stressed) ---"
./run_ipc_benchmark.sh "$DURATION"
IPC_STRESS=$(ls -td results/ipc_* | head -1)
mv "$IPC_STRESS" "$TOP_RESULTS/ipc_stress"

echo ""
echo "--- Shared Memory Benchmark (stressed) ---"
./run_shm_benchmark.sh "$DURATION"
SHM_STRESS=$(ls -td results/shm_* | head -1)
mv "$SHM_STRESS" "$TOP_RESULTS/shm_stress"

stop_stress
capture_memory "phase2_after"

# ==================================================================
# PHASE 3: Extended sustained run (WAL only)
# ==================================================================
SUSTAINED_DURATION=$((DURATION * 10))
if [ "$SUSTAINED_DURATION" -gt 600 ]; then
    SUSTAINED_DURATION=600  # Cap at 10 minutes
fi

echo ""
echo "======================================================"
echo " PHASE 3: Sustained WAL test (${SUSTAINED_DURATION}s)"
echo "======================================================"

capture_memory "phase3_before"

SUSTAINED_DIR="$TOP_RESULTS/wal_sustained"
mkdir -p "$SUSTAINED_DIR"

DB_DIR="/tmp/wal_benchmark"
rm -f "$DB_DIR/test.db" "$DB_DIR/test.db-wal" "$DB_DIR/test.db-shm" "$DB_DIR/test.db-journal"
mkdir -p "$DB_DIR"

SQLITE3="./sqlite3"
[ -x "$SQLITE3" ] || SQLITE3="$(command -v sqlite3)"

$SQLITE3 "$DB_DIR/test.db" "PRAGMA journal_mode=WAL;"
$SQLITE3 "$DB_DIR/test.db" < schema.sql

echo "  Running: c_writer + go_reader + go_writer for ${SUSTAINED_DURATION}s..."

./wal_monitor.sh "$DB_DIR/test.db" "$SUSTAINED_DURATION" > "$SUSTAINED_DIR/wal_size.csv" &
WAL_MON_PID=$!

./c_writer "$DB_DIR/test.db" 30 "$SUSTAINED_DURATION" > "$SUSTAINED_DIR/c_writer.json" &
C_PID=$!

./go_reader "$DB_DIR/test.db" 3 100 "$SUSTAINED_DURATION" > "$SUSTAINED_DIR/go_reader.json" &
GR_PID=$!

./go_writer "$DB_DIR/test.db" 500 "$SUSTAINED_DURATION" > "$SUSTAINED_DIR/go_writer.json" &
GW_PID=$!

wait $C_PID || true
wait $GR_PID || true
wait $GW_PID || true
kill $WAL_MON_PID 2>/dev/null || true
wait $WAL_MON_PID 2>/dev/null || true

ls -la "$DB_DIR/test.db"* > "$SUSTAINED_DIR/db_sizes.txt" 2>/dev/null || true

capture_memory "phase3_after"

echo "  Sustained test complete."

# ==================================================================
# PHASE 4: inotify reliability degradation tests
# ==================================================================
RELIABILITY_DURATION=$((DURATION / 2))
if [ "$RELIABILITY_DURATION" -lt 15 ]; then
    RELIABILITY_DURATION=15
fi

echo ""
echo "======================================================"
echo " PHASE 4: inotify reliability tests (${RELIABILITY_DURATION}s)"
echo "======================================================"

capture_memory "phase4_before"

./run_inotify_reliability.sh "$RELIABILITY_DURATION"
INO_REL=$(ls -td results/inotify_reliability_* | head -1)
mv "$INO_REL" "$TOP_RESULTS/inotify_reliability"

capture_memory "phase4_after"

echo "  Reliability tests complete."

# ==================================================================
# Summary
# ==================================================================
echo ""
echo "======================================================"
echo " All phases complete!"
echo " Finished: $(date -u)"
echo " Results:  $TOP_RESULTS"
echo "======================================================"
echo ""
echo " Directory structure:"
find "$TOP_RESULTS" -type f | sort | sed 's|^|   |'
echo ""
echo " Result counts:"
echo "   WAL clean:       $(find "$TOP_RESULTS/wal_clean" -name '*.json' | wc -l) JSON files"
echo "   WAL stress:      $(find "$TOP_RESULTS/wal_stress" -name '*.json' | wc -l) JSON files"
echo "   WAL sustained:   $(find "$TOP_RESULTS/wal_sustained" -name '*.json' | wc -l) JSON files"
echo "   inotify clean:   $(find "$TOP_RESULTS/inotify_clean" -name '*.json' | wc -l) JSON files"
echo "   inotify stress:  $(find "$TOP_RESULTS/inotify_stress" -name '*.json' | wc -l) JSON files"
echo "   inotify reliab:  $(find "$TOP_RESULTS/inotify_reliability" -name '*.json' | wc -l) JSON files"
echo "   IPC clean:       $(find "$TOP_RESULTS/ipc_clean" -name '*.json' | wc -l) JSON files"
echo "   IPC stress:      $(find "$TOP_RESULTS/ipc_stress" -name '*.json' | wc -l) JSON files"
echo "   SHM clean:       $(find "$TOP_RESULTS/shm_clean" -name '*.json' | wc -l) JSON files"
echo "   SHM stress:      $(find "$TOP_RESULTS/shm_stress" -name '*.json' | wc -l) JSON files"
echo "   Memory:          $(find "$TOP_RESULTS/memory" -type f | wc -l) snapshots"
