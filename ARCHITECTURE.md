# Measurement Methodology

> Part of [IPC You Might Not Need](README.md). See also:
> [Integration Guide](INTEGRATION-GUIDE.md) | [Throughput Scaling Guide](THROUGHPUT-GUIDE.md) | [Example Report](example/EXAMPLE-REPORT.md)

This document describes the exact measurement techniques used by each benchmark
program. Understanding these ensures you can trust the numbers and adapt them
for your specific needs.

---

## Clock Selection

| Measurement | Clock | Rationale |
|---|---|---|
| Same-process latency (e.g., time to INSERT one row) | `CLOCK_MONOTONIC` (C) / `time.Now()` monotonic (Go) | Not affected by NTP adjustments |
| Cross-process latency (e.g., writer→watcher notification time) | `CLOCK_REALTIME` (C) / `time.Now().UnixNano()` (Go) | Both processes read the same wall clock; zero skew on same device |

**Why not always CLOCK_MONOTONIC?** Two separate processes have independent monotonic
clock references. Subtracting one process's monotonic timestamp from another's is meaningless.
CLOCK_REALTIME has NTP skew risk on distributed systems, but on a single embedded device
the risk is negligible compared to the error from using the wrong clock entirely.

---

## Percentile Computation

All programs use the same algorithm:

```
Given: latency array L of length N, sorted ascending

p50 = L[floor(N * 0.50)]
p95 = L[floor(N * 0.95)]
p99 = L[floor(N * 0.99)]
max = L[N - 1]
min = L[0]

If N == 0, all percentiles = 0
```

**C implementation:** `qsort()` on a dynamically allocated `int64_t*` array
(doubling capacity on realloc). Index computation after sort.

**Go implementation:** `sort.Slice()` on `[]int64`. Index computation after sort.

---

## SQLite Measurement

### What We Measure

The timed interval wraps only the `sqlite3_step()` call (C) or `stmt.ExecContext()`/
`db.QueryContext()` call (Go). This captures:
- Lock acquisition time
- WAL append time (for writes)
- Page read time (for reads)
- Codec overhead
- Any busy-wait retry time within the busy handler

### What We Don't Measure

- `sqlite3_prepare_v2()` (done once before the loop)
- `sqlite3_bind_*()` (trivial — nanosecond overhead)
- Application-level data generation (done before timing starts)

### Controlled Variables

- **busy_timeout**: Set via environment variable `BUSY_TIMEOUT` (default 5000ms).
  This controls how long SQLite internally retries on lock contention before
  returning SQLITE_BUSY. Set to 0 for "no retry" baseline measurements.

- **Journal mode**: Set by the orchestrator before benchmarks start. The C writer
  opens with bare `sqlite3_open()` and does NOT set journal_mode — it inherits
  whatever the orchestrator configured, exactly matching real firmware behavior.

- **Random seed**: Fixed at 42 for reproducibility across runs.

### SQLITE_BUSY Handling

When `sqlite3_step()` returns SQLITE_BUSY:
- Increment the busy counter
- Do NOT retry (the busy_timeout already retried internally)
- Continue to the next write
- Log to stderr for debugging

This matches how most embedded applications handle database contention:
the busy handler absorbs brief contention, and genuine overload shows
up as accumulated BUSY counts.

---

## inotify Measurement

### Atomic Rename Pattern

The writer uses a two-step atomic write:
1. Write timestamp + payload to a temp file (`.subsystem.tmp`)
2. `rename()` the temp file to the final name (`subsystem`)

The watcher monitors for `IN_MOVED_TO` events only (not `IN_CREATE` or `IN_MODIFY`),
because `rename()` is atomic at the filesystem level — the watcher never sees a
partial write.

### Latency Measurement

```
Writer process:
  timestamp = CLOCK_REALTIME
  write(temp_file, timestamp + "\n" + payload)
  rename(temp_file → final_name)

Watcher process:
  event = inotify_read()           ← blocks until event
  handler_time = CLOCK_REALTIME    ← first thing in handler
  read(final_name) → parse timestamp from content
  dispatch_latency = handler_time - writer_timestamp
```

Three latencies are measured per event:
1. **Dispatch latency**: Time from writer's rename to watcher's handler entry (notification only)
2. **Processing latency**: Time to parse payload, compare to cache, apply (same-process, CLOCK_MONOTONIC)
3. **Pipeline latency**: End-to-end from writer's timestamp to processing complete (CLOCK_REALTIME)

---

## IPC Socket Measurement

### Protocol

```
Message format: "subsystem_name:timestamp_ns:key1=val1|key2=val2|...\n"
Transport: Unix domain socket (SOCK_STREAM)
```

The Go client sends messages; the C server receives and processes them.
The config payload and processing logic are identical to the inotify benchmark
for apples-to-apples comparison.

### Latency Measurement

```
Client process:
  timestamp = CLOCK_REALTIME
  write(socket, "subsystem:" + timestamp + ":" + payload + "\n")

Server process:
  read(socket) → parse message
  recv_time = CLOCK_REALTIME  ← immediately after read returns
  dispatch_latency = recv_time - client_timestamp
```

Same three-tier latency measurement as inotify (dispatch, processing, pipeline).

---

## Shared Memory (mmap + FIFO) Measurement

### Transport Architecture

```
Writer process (Go):                    Reader process (C):
  shm_open("/bench_shm")                 shm_open("/bench_shm")
  mmap(MAP_SHARED)                        ftruncate + mmap(MAP_SHARED)
                                          mkfifo("/tmp/bench_shm_fifo")
  open("/tmp/bench_shm_fifo", O_WRONLY)
        │                                        │
        │   Shared memory layout (536 bytes):     │
        │   ┌──────────────────────────────┐      │
        │   │ ready     (uint32, atomic)   │      │
        │   │ sequence  (uint32)           │      │
        │   │ timestamp (int64)            │      │
        │   │ payload_length (uint32)      │      │
        │   │ subsystem_id   (uint32)      │      │
        │   │ payload[512]   (char)        │      │
        │   └──────────────────────────────┘      │
        │                                         │
  atomic_store(ready, seq)  ──────────────→  atomic_load(ready) != 0
  write(fifo, 1)            ──────────────→  read(fifo) blocks until signaled
```

The reader creates the shared memory segment and a named FIFO at
`/tmp/bench_shm_fifo`. The writer opens the same shm segment and
the FIFO for writing. One byte per message signals the reader.

### Memory Ordering (ARMv7)

ARMv7 is weakly ordered. The ready flag uses explicit release/acquire semantics:

- **Writer (Go):** `sync/atomic.StoreUint32(readyPtr, seq)` — issues a DMB (data
  memory barrier) on ARM, ensuring all preceding writes (payload, timestamp, etc.)
  are visible before the ready flag becomes non-zero.
- **Reader (C):** `__atomic_load_n(&msg->ready, __ATOMIC_ACQUIRE)` — issues a DMB
  after the load, ensuring subsequent reads of payload data see the writer's stores.

This is critical: without barriers, the reader could see `ready != 0` but read
stale payload data. x86 TSO would mask this bug; ARM exposes it.

### Struct Layout (`shm_common.h`)

```
Offset  Size  Type       Field
  0       4   uint32_t   ready          (0 = free, >0 = sequence number)
  4       4   uint32_t   sequence
  8       8   int64_t    timestamp_ns   (CLOCK_REALTIME, writer pre-write)
 16       4   uint32_t   payload_length
 20       4   uint32_t   subsystem_id   (0=sensor, 1=network, 2=user)
 24     512   char       payload        (null-terminated)
────────────────────────────────────────
536 bytes total, naturally aligned for ARM
```

All fields use fixed-width types. No `int`, `long`, `size_t` or `bool`. Six
`_Static_assert` checks verify offsets at compile time.

### Latency Measurement

```
Writer process (Go):
  timestamp = time.Now().UnixNano()          ← CLOCK_REALTIME
  write payload + timestamp to shared memory
  atomic_store(ready, sequence)              ← release barrier
  write(fifo, 1)                             ← signal reader

Reader process (C):
  read(fifo)                                 ← blocks until signal
  atomic_load(ready)                         ← acquire barrier
  memcpy local copy of message
  atomic_store(ready, 0)                     ← release: buffer free
  recv_time = CLOCK_REALTIME
  dispatch_latency = recv_time - writer_timestamp
```

Same three-tier latency measurement as inotify and IPC:
1. **Dispatch latency**: Writer's pre-write timestamp to reader's post-receive timestamp
2. **Processing latency**: Time to parse payload + apply config (CLOCK_MONOTONIC)
3. **Pipeline latency**: Writer's pre-write timestamp to processing complete (CLOCK_REALTIME)

### Sequence Integrity

The reader tracks sequence errors: if the received sequence number is not
exactly `previous + 1`, an error is counted. Unlike inotify (which silently
coalesces events) or sockets (which buffer in the kernel), shared memory
overwrites in place — if the writer posts faster than the reader consumes,
a `buffer_busy_count` is incremented on the writer side.

---

## inotify Reliability Testing

### What We Test

Five failure-mode scenarios that stress inotify's inherent limitations:

| Scenario | Stressor | What It Reveals |
|---|---|---|
| **Queue overflow** | 1ms writes + 50ms reader delay | `IN_Q_OVERFLOW` events when kernel queue fills (default 16384 events) |
| **I/O storm** | `dd` reads during 100ms writes | VFS lock contention affecting notification latency |
| **Rapid overwrite** | 5 writes per tick at 100ms | Event coalescing in the kernel (only last write notified) |
| **fsync race** | No `fsync()` before rename + 10ms reader delay | Whether reader sees partial/empty files |
| **tmpfs vs block** | Same workload on tmpfs vs eMMC | Filesystem-dependent latency delta |

### Measurement Additions

The enhanced watcher tracks two new counters:
- **`overflow_events`**: Number of `IN_Q_OVERFLOW` events received (watcher
  adds `IN_Q_OVERFLOW` to the inotify_add_watch mask)
- **`coalesced_events`**: Number of sequence gaps detected between consecutive
  events (writer embeds an incrementing sequence number; gaps indicate the
  kernel merged multiple notifications)

### Queue Overflow Detection

The inotify event loop checks `event->mask & IN_Q_OVERFLOW` on every event.
When overflow occurs, the kernel has discarded events — the watcher cannot
know which files changed. This is a fundamental limitation of inotify for
safety-critical config delivery.

### Writer Enhancements

- **`--no-sync`**: Skips `f.Sync()` before rename, testing whether the reader
  sees consistent file content without explicit fsync
- **`--burst-pairs N`**: Writes N rapid sequential updates per tick, forcing
  the kernel to either deliver N events or coalesce them
- **Sequence numbers**: Each file write includes an incrementing sequence number
  as the second line, enabling the watcher to detect gaps

---

## Data Generation

All benchmark programs use deterministic random data seeded with `srand(42)` (C)
or `rand.NewSource(42)` (Go) for reproducibility:

| Field | Range | Purpose |
|---|---|---|
| Record ID | Incrementing from 1 | Primary identifier |
| Target values | Random 50–500, 0–360 | Simulates sensor targets |
| Result flag | "P" (80%) or "F" (20%) | Pass/fail distribution |
| Actual values | Target ± 10% | Simulates measurement noise |
| Category ID | Cycling 1–5 | For WHERE clause selectivity |
| Duration | Random 100–5000 ms | Variable-length operations |
| Coordinates | Fixed realistic values | Static metadata fields |

### Config Payloads (inotify, IPC, and Shared Memory)

Payloads are pipe-separated key=value pairs:
```
date=2024-03-15|direction=1|mode=0|preset=3|logging=1|counter0=247|...
```

Three subsystem types with different payload sizes:
- `sensor_calibration`: ~350 bytes (20 fields)
- `network_config`: ~200 bytes (10 fields)
- `user_profiles`: ~150 bytes (9 fields)

Values vary per call to simulate real config changes. The watcher/server
caches previous state and counts fields that actually changed.

---

## Stress Test Methodology

The stress test phase applies synthetic load to simulate resource-constrained conditions:

| Stress Type | Implementation | Purpose |
|---|---|---|
| CPU | 2 busy loops running `sha256sum` | Saturate available cores |
| I/O | Continuous `dd` reads from storage | Compete for disk bandwidth |
| Memory | Allocate block in tmpfs | Reduce available RAM, increase swap pressure |

Benchmarks run identically in both clean and stressed phases. Comparing results
quantifies how resilient each approach is under system load — critical for embedded
systems where CPU and memory headroom is limited.

---

## JSON Output Convention

- One JSON object per program run, to stdout
- Progress and debug messages to stderr only
- All field names are `snake_case`
- Latency units specified per benchmark (µs for WAL, ns for inotify/IPC)
- All numeric values are integers
- Programs exit cleanly on SIGINT/SIGTERM, printing JSON before exit

This convention enables simple orchestration:
```bash
./c_writer test.db 30 60 > results/writer.json 2>logs/writer.log
```

---

## Signal Handling

### C programs
- Install `SIGINT` and `SIGTERM` handlers using `sigaction()`
- Handler sets `volatile sig_atomic_t g_stop = 1`
- Main loop checks `g_stop` each iteration
- After loop exit: finalize statements, close DB/socket, print JSON, return 0

### Go programs
- `signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)`
- Signal cancels context → goroutines exit their select loops
- After `wg.Wait()`: compute stats, marshal JSON, print, exit 0

Clean shutdown ensures complete JSON output even when terminated early.
