#!/bin/bash
# wal_monitor.sh — Samples WAL file size every second
#
# Usage: ./wal_monitor.sh <db_path> <duration_sec>
# Output: CSV to stdout (timestamp_ms, wal_size_bytes)
#
# Uses POSIX-compatible commands with busybox fallbacks.

DB_PATH="$1"
DURATION="$2"
WAL_PATH="${DB_PATH}-wal"

echo "timestamp_ms,wal_size_bytes"

i=0
while [ "$i" -lt "$DURATION" ]; do
    if [ -f "$WAL_PATH" ]; then
        # Try GNU stat first, fall back to busybox/POSIX
        SIZE=$(stat -c%s "$WAL_PATH" 2>/dev/null || stat -f%z "$WAL_PATH" 2>/dev/null || wc -c < "$WAL_PATH")
    else
        SIZE=0
    fi

    # Timestamp: try date +%s%3N (GNU), fall back to seconds * 1000
    TS=$(date +%s%3N 2>/dev/null)
    if [ ${#TS} -le 10 ]; then
        TS=$(($(date +%s) * 1000))
    fi

    echo "${TS},${SIZE}"
    sleep 1
    i=$((i + 1))
done
