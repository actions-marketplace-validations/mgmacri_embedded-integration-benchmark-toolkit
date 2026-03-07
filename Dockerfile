# Dockerfile — Cross-compilation environment for integration benchmarks
# Target: ARM Linux (armv7l)
#
# Usage:
#   docker build -t bench-build .
#   docker run --rm -v ${PWD}:/work bench-build make all
#   docker run --rm -v ${PWD}:/work bench-build make native    # x86 build
#   docker run --rm -v ${PWD}:/work bench-build make cross     # ARM build
#
# Or deploy directly:
#   docker run --rm -v ${PWD}:/work bench-build make deploy TARGET_IP=192.168.1.100

FROM golang:1.22-bookworm AS builder

# ARM cross-compiler (amd64 package that outputs ARM code)
RUN apt-get update -qq && \
    apt-get install -y -qq --no-install-recommends \
        gcc-arm-linux-gnueabihf \
        libc6-dev-armhf-cross \
        make \
        wget \
        unzip && \
    rm -rf /var/lib/apt/lists/*

# SQLite amalgamation — compiled directly into C binaries
ARG SQLITE_VERSION=3490100
ARG SQLITE_YEAR=2025
RUN mkdir -p /opt/sqlite && \
    wget -q "https://www.sqlite.org/${SQLITE_YEAR}/sqlite-amalgamation-${SQLITE_VERSION}.zip" \
         -O /tmp/sqlite.zip && \
    unzip -oq /tmp/sqlite.zip -d /tmp/sqlite_tmp && \
    cp /tmp/sqlite_tmp/sqlite-amalgamation-${SQLITE_VERSION}/sqlite3.c /opt/sqlite/ && \
    cp /tmp/sqlite_tmp/sqlite-amalgamation-${SQLITE_VERSION}/sqlite3.h /opt/sqlite/ && \
    cp /tmp/sqlite_tmp/sqlite-amalgamation-${SQLITE_VERSION}/shell.c /opt/sqlite/ && \
    rm -rf /tmp/sqlite.zip /tmp/sqlite_tmp

WORKDIR /work

# Ensure ARM cross-compiler is used for C programs
ENV CC=arm-linux-gnueabihf-gcc

# Cache Go module downloads
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true

# Default: build everything
CMD ["make", "all", "SQLITE_DIR=/opt/sqlite"]
