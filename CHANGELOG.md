# Changelog

All notable changes to this project will be documented in this file.

## [1.0.0] — 2025-06-20

Initial public release.

### Benchmarks

- **SQLite WAL contention** — C writer + Go reader/writer, 6 scenarios (WAL vs rollback,
  escalating write rates 33–330 w/s, 1 or 3 concurrent readers), 10-minute sustained test
- **inotify sentinel files** — Go writer + C watcher, atomic rename delivery, dispatch/
  processing/pipeline latency measurement
- **Unix domain sockets (IPC)** — Go client + C server, identical payloads and config
  parsing as inotify for direct comparison
- **Shared memory (mmap + FIFO)** — Go writer + C reader, ARMv7 memory barriers,
  release/acquire semantics, sequence integrity tracking
- **inotify reliability** — 5 failure-mode scenarios: queue overflow, I/O storm,
  rapid overwrite (coalescing), fsync race, tmpfs vs block device

### Tooling

- `bench` CLI orchestrator — YAML-driven configuration, schema introspection,
  runtime config bridge (`/tmp/bench_runtime.json`)
- Verdict-first report generator (Go) with pass/fail scorecard and complexity
  signal analysis
- Python report generator (alternative, no Go dependency)
- Docker-based cross-compilation (`arm-linux-gnueabihf-gcc` + `GOOS=linux GOARCH=arm`)
- Deploy/collect shell scripts for target device workflow
- Clean + stress test phases (CPU, I/O, memory pressure)

### Shared Infrastructure

- `benchmark/common/` — C headers: `latency.h`, `subsystem.h`, `bench_runtime.h`
- `benchmark/internal/` — Go packages: `config`, `schema`, `payload`, `stats`,
  `runtime`, `report`
- Consistent measurement methodology across all benchmarks
  (CLOCK_REALTIME cross-process, CLOCK_MONOTONIC same-process, floor-index percentiles)
