#!/bin/bash
# deploy.sh — Deploy pre-built binaries and scripts to the target device
#
# Run this after building with Docker:
#   docker build -t bench-build .
#   docker run --rm -v $(pwd):/work bench-build
#   ./deploy.sh 192.168.1.100
#
# Or after a local build:
#   ./setup.sh && make all CC=arm-linux-gnueabihf-gcc
#   ./deploy.sh 192.168.1.100

set -e

TARGET_IP="${1:?Usage: ./deploy.sh <target_ip> [target_dir] [target_user]}"
TARGET_DIR="${2:-/tmp/benchmark}"
TARGET_USER="${3:-root}"

echo "Deploying to ${TARGET_USER}@${TARGET_IP}:${TARGET_DIR}/"

# Verify binaries exist
if [ ! -f build/c_writer ]; then
    echo "ERROR: build/ directory is empty. Build first:" >&2
    echo "  docker build -t bench-build . && docker run --rm -v \$(pwd):/work bench-build" >&2
    exit 1
fi

# Create target directory
ssh "${TARGET_USER}@${TARGET_IP}" "mkdir -p ${TARGET_DIR}"

# Copy binaries
echo "  Copying binaries..."
scp build/c_writer build/go_reader build/go_writer \
    build/inotify_watcher build/sentinel_writer \
    build/ipc_server build/ipc_client \
    build/shm_reader build/shm_writer \
    build/generate_report build/sqlite3 \
    "${TARGET_USER}@${TARGET_IP}:${TARGET_DIR}/"

# Copy scripts
echo "  Copying scripts..."
scp benchmark/run_all.sh \
    benchmark/wal/run_wal_benchmark.sh \
    benchmark/wal/wal_monitor.sh \
    benchmark/inotify/run_inotify_benchmark.sh \
    benchmark/inotify/run_inotify_reliability.sh \
    benchmark/ipc/run_ipc_benchmark.sh \
    benchmark/shm/run_shm_benchmark.sh \
    schema.sql \
    "${TARGET_USER}@${TARGET_IP}:${TARGET_DIR}/"

# Make scripts executable
ssh "${TARGET_USER}@${TARGET_IP}" "chmod +x ${TARGET_DIR}/*.sh"

echo ""
echo "Done. Run on target:"
echo "  ssh ${TARGET_USER}@${TARGET_IP}"
echo "  cd ${TARGET_DIR}"
echo "  ./run_all.sh 60    # full suite, 60s per scenario"
