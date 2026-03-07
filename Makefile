# Makefile — Integration Benchmark Toolkit
#
# Cross-compiles all C and Go programs for an ARM Linux target.
# Output binaries go into build/ to avoid clashing with source directories.
#
# Usage:
#   make all           — build everything
#   make c-programs    — build C programs only
#   make go-programs   — build Go programs only
#   make deploy        — scp all binaries + scripts to target
#   make clean         — remove built binaries
#
# Set CC to your cross-compiler (default: arm-linux-gnueabihf-gcc):
#   make CC=arm-linux-gnueabihf-gcc
#
# For native builds (testing on x86):
#   make CC=gcc GOOS= GOARCH= GOARM=

# --- Toolchain ---
CC        ?= arm-linux-gnueabihf-gcc
CFLAGS    ?= -O2 -Wall -Wextra

# SQLite amalgamation — compiled directly into C binaries (no target libsqlite3 needed)
SQLITE_DIR  = third_party/sqlite
SQLITE_SRC  = $(SQLITE_DIR)/sqlite3.c
SQLITE_INC  = -I$(SQLITE_DIR)
SQLITE_DEFS = -DSQLITE_THREADSAFE=1 -DSQLITE_ENABLE_WAL=1
LDLIBS_SQLITE = -lm -lpthread -ldl

# Go cross-compile settings (override for your target architecture)
export GOOS     = linux
export GOARCH   = arm
export GOARM    = 7
export CGO_ENABLED = 1
export CC

# --- Source directories ---
WAL_DIR    = benchmark/wal
INO_DIR    = benchmark/inotify
IPC_DIR    = benchmark/ipc
SHM_DIR    = benchmark/shm
REPORT_DIR = benchmark/report

# --- Output directory ---
BUILD_DIR = build

# --- Output binaries (all in build/) ---
C_WRITER          = $(BUILD_DIR)/c_writer
GO_READER         = $(BUILD_DIR)/go_reader
GO_WRITER         = $(BUILD_DIR)/go_writer
INOTIFY_WATCHER   = $(BUILD_DIR)/inotify_watcher
SENTINEL_WRITER   = $(BUILD_DIR)/sentinel_writer
IPC_SERVER        = $(BUILD_DIR)/ipc_server
IPC_CLIENT        = $(BUILD_DIR)/ipc_client
SHM_READER        = $(BUILD_DIR)/shm_reader
SHM_WRITER        = $(BUILD_DIR)/shm_writer
REPORT_GEN        = $(BUILD_DIR)/generate_report
SQLITE3_CLI       = $(BUILD_DIR)/sqlite3

BENCH_CLI         = $(BUILD_DIR)/bench

C_PROGRAMS  = $(C_WRITER) $(INOTIFY_WATCHER) $(IPC_SERVER) $(SHM_READER) $(SQLITE3_CLI)
GO_PROGRAMS = $(GO_READER) $(GO_WRITER) $(SENTINEL_WRITER) $(IPC_CLIENT) $(SHM_WRITER) $(REPORT_GEN) $(BENCH_CLI)

# Common headers (for dependency tracking)
COMMON_DIR  = benchmark/common
COMMON_HDRS = $(COMMON_DIR)/latency.h $(COMMON_DIR)/subsystem.h $(COMMON_DIR)/bench_runtime.h

# ===================================================================
.PHONY: all c-programs go-programs clean deploy native cross

all: c-programs go-programs

c-programs: $(BUILD_DIR) $(C_PROGRAMS)

go-programs: $(BUILD_DIR) $(GO_PROGRAMS)

# Native build (for x86 testing — overrides cross-compile defaults)
native:
	$(MAKE) CC=gcc GOOS= GOARCH= GOARM= CGO_ENABLED=1

# Explicit cross-compile target
cross:
	$(MAKE) CC=arm-linux-gnueabihf-gcc GOOS=linux GOARCH=arm GOARM=7

$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

# --- C targets ---

$(C_WRITER): $(WAL_DIR)/c_writer.c $(SQLITE_SRC) $(COMMON_HDRS)
	$(CC) $(CFLAGS) $(SQLITE_INC) $(SQLITE_DEFS) -o $@ $< $(SQLITE_SRC) $(LDLIBS_SQLITE)

$(INOTIFY_WATCHER): $(INO_DIR)/watcher.c $(COMMON_HDRS)
	$(CC) $(CFLAGS) -o $@ $<

$(IPC_SERVER): $(IPC_DIR)/server.c $(COMMON_HDRS)
	$(CC) $(CFLAGS) -o $@ $<

$(SHM_READER): $(SHM_DIR)/reader.c $(SHM_DIR)/shm_common.h $(COMMON_HDRS)
	$(CC) $(CFLAGS) -o $@ $< -lrt -lpthread

$(SQLITE3_CLI): $(SQLITE_DIR)/shell.c $(SQLITE_SRC)
	$(CC) $(CFLAGS) $(SQLITE_INC) $(SQLITE_DEFS) -DSQLITE_OMIT_READLINE -o $@ $< $(SQLITE_SRC) $(LDLIBS_SQLITE)

# --- Go targets ---
# CGO_ENABLED=1 for SQLite programs, CGO_ENABLED=0 for pure Go

# Go internal packages (for dependency tracking)
GO_INTERNAL = benchmark/internal/stats/stats.go \
              benchmark/internal/payload/payload.go \
              benchmark/internal/runtime/runtime.go \
              benchmark/internal/config/config.go \
              benchmark/internal/schema/schema.go \
              benchmark/internal/report/report.go

$(GO_READER): $(WAL_DIR)/go_reader/go_reader.go $(GO_INTERNAL)
	CGO_ENABLED=1 go build -o $@ ./$(WAL_DIR)/go_reader

$(GO_WRITER): $(WAL_DIR)/go_writer/go_writer.go $(GO_INTERNAL)
	CGO_ENABLED=1 go build -o $@ ./$(WAL_DIR)/go_writer

$(SENTINEL_WRITER): $(INO_DIR)/writer/writer.go $(GO_INTERNAL)
	CGO_ENABLED=0 go build -o $@ ./$(INO_DIR)/writer

$(IPC_CLIENT): $(IPC_DIR)/client/client.go $(GO_INTERNAL)
	CGO_ENABLED=0 go build -o $@ ./$(IPC_DIR)/client

$(SHM_WRITER): $(SHM_DIR)/writer/writer.go $(GO_INTERNAL)
	CGO_ENABLED=0 go build -o $@ ./$(SHM_DIR)/writer

$(REPORT_GEN): $(REPORT_DIR)/cmd/generate_report.go
	CGO_ENABLED=0 go build -o $@ ./$(REPORT_DIR)/cmd

$(BENCH_CLI): benchmark/bench/main.go $(GO_INTERNAL)
	CGO_ENABLED=0 go build -o $@ ./benchmark/bench

# --- Deployment ---

TARGET_IP   ?= 192.168.1.100
TARGET_DIR  ?= /tmp/benchmark
TARGET_USER ?= root

SCRIPTS = $(WAL_DIR)/wal_monitor.sh \
          $(WAL_DIR)/run_wal_benchmark.sh \
          $(INO_DIR)/run_inotify_benchmark.sh \
          $(INO_DIR)/run_inotify_reliability.sh \
          $(IPC_DIR)/run_ipc_benchmark.sh \
          $(SHM_DIR)/run_shm_benchmark.sh \
          benchmark/run_all.sh

deploy: all
	scp $(C_PROGRAMS) $(GO_PROGRAMS) \
	    $(SCRIPTS) schema.sql \
	    bench.example.yaml bench.minimal.yaml \
	    $(TARGET_USER)@$(TARGET_IP):$(TARGET_DIR)/

# --- Clean ---

clean:
	rm -rf $(BUILD_DIR)
