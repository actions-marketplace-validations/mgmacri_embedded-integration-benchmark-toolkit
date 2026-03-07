#!/bin/bash
# setup.sh — Download dependencies for local (non-Docker) builds
#
# Downloads the SQLite amalgamation and generates go.sum.
# Run this once before 'make all' when building without Docker.
#
# The Docker build handles this automatically — you only need this
# script if you want to build directly on your host.
set -e

SQLITE_VERSION="3490100"
SQLITE_YEAR="2025"
SQLITE_URL="https://www.sqlite.org/${SQLITE_YEAR}/sqlite-amalgamation-${SQLITE_VERSION}.zip"
SQLITE_DIR="third_party/sqlite"

echo "=== Integration Benchmark Toolkit — Setup ==="

# --- SQLite Amalgamation ---
if [ -f "${SQLITE_DIR}/sqlite3.c" ] && [ -f "${SQLITE_DIR}/sqlite3.h" ]; then
    echo "[OK] SQLite amalgamation already present in ${SQLITE_DIR}/"
else
    echo "Downloading SQLite amalgamation ${SQLITE_VERSION}..."
    mkdir -p "${SQLITE_DIR}"

    TMPDIR=$(mktemp -d)
    trap "rm -rf '$TMPDIR'" EXIT

    if command -v wget >/dev/null 2>&1; then
        wget -q "${SQLITE_URL}" -O "${TMPDIR}/sqlite.zip"
    elif command -v curl >/dev/null 2>&1; then
        curl -sL "${SQLITE_URL}" -o "${TMPDIR}/sqlite.zip"
    else
        echo "ERROR: Neither wget nor curl found. Install one and re-run." >&2
        exit 1
    fi

    if command -v unzip >/dev/null 2>&1; then
        unzip -oq "${TMPDIR}/sqlite.zip" -d "${TMPDIR}"
    else
        echo "ERROR: unzip not found. Install it and re-run." >&2
        exit 1
    fi

    SRC_DIR="${TMPDIR}/sqlite-amalgamation-${SQLITE_VERSION}"
    cp "${SRC_DIR}/sqlite3.c"  "${SQLITE_DIR}/"
    cp "${SRC_DIR}/sqlite3.h"  "${SQLITE_DIR}/"
    cp "${SRC_DIR}/shell.c"    "${SQLITE_DIR}/"

    echo "[OK] SQLite amalgamation installed to ${SQLITE_DIR}/"
fi

# --- Go module dependencies ---
if [ -f "go.sum" ]; then
    echo "[OK] go.sum already present"
else
    echo "Running go mod tidy..."
    go mod tidy
    echo "[OK] go.sum generated"
fi

echo ""
echo "Setup complete. Build with:"
echo "  make all                          # cross-compile for ARM"
echo "  make all CC=gcc GOOS= GOARCH= GOARM=  # native build for testing"
