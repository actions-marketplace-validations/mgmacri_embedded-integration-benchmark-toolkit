#!/bin/bash
# collect_results.sh — Pull results from target and generate report
#
# Usage: ./collect_results.sh <target_ip> [target_dir] [target_user]
#
# This copies the results directory back from the target device,
# then runs the Python report generator to produce a markdown report.

set -e

TARGET_IP="${1:?Usage: ./collect_results.sh <target_ip> [target_dir] [target_user]}"
TARGET_DIR="${2:-/tmp/benchmark}"
TARGET_USER="${3:-root}"

echo "Collecting results from ${TARGET_USER}@${TARGET_IP}:${TARGET_DIR}/results/"

# Find the most recent results directory on target
RESULTS_NAME=$(ssh "${TARGET_USER}@${TARGET_IP}" \
    "ls -td ${TARGET_DIR}/results/final_* 2>/dev/null | head -1 | xargs basename" 2>/dev/null || true)

if [ -z "$RESULTS_NAME" ]; then
    echo "No final_* results directory found. Copying all results..."
    mkdir -p results
    scp -r "${TARGET_USER}@${TARGET_IP}:${TARGET_DIR}/results/" .
else
    echo "Latest results: $RESULTS_NAME"
    mkdir -p "results/${RESULTS_NAME}"
    scp -r "${TARGET_USER}@${TARGET_IP}:${TARGET_DIR}/results/${RESULTS_NAME}/" "results/${RESULTS_NAME}"
fi

echo ""
echo "Results collected to results/"

# Generate report if Python is available
if command -v python3 >/dev/null 2>&1; then
    RESULTS_PATH="results/${RESULTS_NAME:-$(ls -td results/final_* 2>/dev/null | head -1)}"
    if [ -d "$RESULTS_PATH" ]; then
        echo "Generating report..."
        python3 generate_report.py "$RESULTS_PATH" > "BENCHMARK-REPORT.md"
        echo "Report written to BENCHMARK-REPORT.md"
    fi
else
    echo "Python3 not found — run manually:"
    echo "  python3 generate_report.py results/${RESULTS_NAME}"
fi
