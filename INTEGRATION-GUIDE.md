# Integration Architecture Decision Guide

> Part of [IPC You Might Not Need](README.md). See also:
> [Measurement Methodology](ARCHITECTURE.md) | [Throughput Scaling Guide](THROUGHPUT-GUIDE.md) | [Example Report](example/EXAMPLE-REPORT.md)

Use this guide **after** running the benchmarks on your target hardware. Your numbers
tell you which integration pattern fits your project — no guessing needed.

---

## Step 1: Assess Database Contention (WAL Benchmark Results)

Look at your WAL benchmark JSON output. The critical field is `sqlite_busy_count`.

### SQLITE_BUSY = 0 across all WAL scenarios

**Your database access has no contention at the tested throughput.**

This means:
- Multiple processes can safely share one SQLite file using WAL mode
- No IPC layer, message queue, or database server is needed for data safety
- You save the engineering cost of building and maintaining an IPC protocol

**Recommended architecture:**
```
Process A (C/C++)  ──writes──→  SQLite WAL DB  ←──reads──  Process B (Go/Python)
```

People-hours saved: significant. No serialization protocol, no socket management,
no connection handling, no message versioning. SQLite's WAL mode handles everything.

### SQLITE_BUSY > 0 at your normal write rate

**You have contention.** This means your concurrent write rate exceeds what WAL's
built-in busy_timeout can absorb. Options:

1. **Increase busy_timeout** — Try `BUSY_TIMEOUT=10000` (10 seconds). If BUSY drops
   to zero, the contention was just occasional lock timing. Cost: some writes take
   longer during contention windows.

2. **Reduce write frequency** — Can you batch writes? Buffer 10 samples and INSERT
   in a single transaction instead of 10 separate transactions?

3. **Separate databases** — Put read-heavy data in one DB, write-heavy data in another.
   No cross-table contention.

4. **Add IPC coordination** — If none of the above work, you need an IPC layer
   (sockets, D-Bus, or a write-serialization service).

### SQLITE_BUSY > 0 only under stress

**Your normal throughput is fine, but system load causes occasional contention.**

This is usually acceptable for non-safety-critical applications. The busy_timeout
mechanism retries automatically — measure whether the retry latency is within your
tolerance by checking `write_latency_us.p99` under stress.

---

## Step 2: Assess Configuration Notifications (inotify vs IPC)

If your application needs one process to notify another about configuration changes
(not database data — think "settings updated, please reload"), compare the inotify
and IPC benchmark results.

### inotify: dispatch_latency_ns.p99 < 1,000,000 (< 1ms)

**inotify sentinel files deliver sub-millisecond notification.**

This is sufficient for:
- Config file changes (settings, calibration parameters)
- State transitions (mode changes, user login events)
- Any notification where <1ms response is acceptable

**Recommended architecture:**
```
Process B ──atomic rename──→  /watch_dir/config_name
                                      │
                              inotify IN_MOVED_TO
                                      ↓
Process A ──reads file──→  parses payload, applies config
```

Advantages:
- No socket management code
- Files persist across crashes (natural durability)
- Standard Linux filesystem semantics
- No custom protocol to version or maintain

### inotify missed_events > 0 at high rates

**inotify is losing events under load.** This happens because:
- Same-named files overwrite before the watcher reads them
- The inotify buffer fills up under burst traffic

If your application can't tolerate missed events:
- **Reduce notification rate** (batch changes)
- **Use unique filenames** (include sequence numbers)
- **Switch to sockets** for guaranteed delivery

### IPC sockets consistently faster than inotify

**Sockets deliver lower latency for your workload.** This is typical for:
- High-frequency notifications (>100/sec sustained)
- Payloads that are already serialized in memory
- Applications that need bidirectional communication
- Systems requiring delivery acknowledgment

**Recommended architecture:**
```
Process B ──socket write──→  Unix Domain Socket  ──socket read──→  Process A
```

Additional complexity:
- Socket lifecycle management (create, bind, listen, accept, close)
- Reconnection logic for crashes
- Message framing (newline-delimited, length-prefixed, etc.)
- Error handling for broken pipes

---

## Step 3: Make the Architecture Decision

### Decision Matrix

| Factor | WAL + inotify | WAL + Sockets | Full IPC |
|---|---|---|---|
| **Database contention** | Zero at your rate | Zero at your rate | N/A — IPC serializes |
| **Config notification** | <1ms p99 | <100µs p99 | <100µs p99 |
| **Reliability** | Files persist | Connection-oriented | Connection-oriented |
| **Complexity** | Lowest | Medium | Highest |
| **People-hours** | ~days | ~weeks | ~weeks-months |
| **Crash recovery** | Automatic (files exist) | Reconnect needed | Reconnect needed |
| **Bidirectional** | No (add 2nd directory) | Yes | Yes |
| **Guaranteed delivery** | No (file overwrite) | Yes (TCP-like) | Yes |
| **Determinism** | Good (<1ms) | Better (<100µs) | Best (tunable) |
| **Attack surface** | Directory permissions + file format (likely already in filesystem threat model) | Socket path + protocol + FD lifecycle (new interface) | Framework-dependent (often large, multiple new interfaces) |
| **Threat model entries** | ~0 incremental (filesystem overlap) | 1+ (new socket interface) | N (one per endpoint/bus) |

### When WAL + inotify Is Enough (Most Embedded Projects)

Your project likely fits this pattern if:
- ✅ Write rate < 100/sec per process
- ✅ Config changes happen < 10/sec
- ✅ <1ms notification latency is acceptable
- ✅ Unidirectional signaling is sufficient
- ✅ Occasional missed events are survivable (config is re-sent)

**This covers the vast majority of embedded data acquisition + web service architectures.**

### When You Need Sockets

Your project needs explicit IPC if:
- ❌ Write rate causes SQLITE_BUSY even with WAL + 5s timeout
- ❌ Need <100µs guaranteed notification latency
- ❌ Need bidirectional request/response
- ❌ Cannot tolerate any missed events
- ❌ Need flow control or backpressure

> **Security cost:** Moving to sockets adds a new IPC interface — a trust
> boundary that didn't exist when you were using inotify. That interface
> needs a threat model entry (attack vectors, data flows, mitigations)
> before it ships. The toolkit's [CI integration](CI-INTEGRATION.md)
> detects this automatically: when `transport-count` increases, it flags
> the change for security review. See [Throughput Guide §Security Cost](THROUGHPUT-GUIDE.md#the-security-cost-of-graduation).

### When You Need a Full IPC Framework (D-Bus, gRPC, etc.)

Consider a framework if:
- ❌ More than 2 processes communicating
- ❌ Need service discovery
- ❌ Need structured RPC with schema evolution
- ❌ Team already uses the framework elsewhere

> **Security cost:** A full IPC framework typically introduces multiple
> interfaces (service bus, RPC endpoints, discovery mechanism). Each is
> a separate trust boundary in your threat model. The engineering cost
> in the table below reflects implementation — the security review cost
> is additional and scales with the number of interfaces added.

---

## Step 4: Estimate People-Hours

Based on measured numbers, here's what each architecture costs to implement:

### WAL + inotify (simplest)

| Task | Estimate |
|---|---|
| Set `PRAGMA journal_mode=WAL` in both processes | 1 hour |
| Set `sqlite3_busy_timeout()` or DSN parameter | 1 hour |
| Create sentinel write/watch directory | 1 hour |
| Implement atomic rename writer (one function) | 2-4 hours |
| Implement inotify watcher (one event loop) | 4-8 hours |
| Testing and edge cases | 4-8 hours |
| **Total** | **~2-3 days** |

### WAL + Unix Sockets

| Task | Estimate |
|---|---|
| All WAL tasks above | 1 day |
| Design message protocol | 2-4 hours |
| Implement socket server (accept, read, parse) | 8-16 hours |
| Implement socket client (connect, write, retry) | 4-8 hours |
| Reconnection and error handling | 4-8 hours |
| Testing (happy path + failure modes) | 8-16 hours |
| **Total** | **~1-2 weeks** |

### Full IPC Framework

| Task | Estimate |
|---|---|
| Select and integrate framework (D-Bus, gRPC, etc.) | 1-2 days |
| Define service interfaces / proto files | 2-4 days |
| Implement server-side handlers | 3-5 days |
| Implement client-side stubs | 2-3 days |
| Error handling, reconnection, timeouts | 2-3 days |
| Testing (unit + integration) | 3-5 days |
| **Total** | **~3-4 weeks** |

---

## Reading Your Results

### WAL Benchmark JSON Fields

| Field | What It Tells You |
|---|---|
| `sqlite_busy_count` | **The most important number.** Zero = no contention. |
| `write_latency_us.p99` | 99th percentile write time in microseconds |
| `write_latency_us.p50` | Typical write time — compare WAL vs rollback |
| `successful_writes / total_writes` | Should be 100% if busy_timeout is working |
| `journal_mode` | Confirms WAL or rollback was active |

### inotify Benchmark JSON Fields

| Field | What It Tells You |
|---|---|
| `dispatch_latency_ns.p99` | 99th percentile notification time in nanoseconds |
| `total_pipeline_latency_ns.p99` | End-to-end: notification + config parse + apply |
| `missed_events` | Should be zero. Non-zero = overload. |
| `config_changes_detected` | Confirms payload parsing worked |
| `events_by_subsystem` | Distribution across your config domains |

### IPC Benchmark JSON Fields

| Field | What It Tells You |
|---|---|
| `dispatch_latency_ns.p99` | 99th percentile socket-to-handler time |
| `total_pipeline_latency_ns.p99` | End-to-end including config processing |
| `total_events` | Total messages processed |

### Comparing inotify vs IPC

The benchmarks use identical:
- Payload format and size
- Config parsing and caching logic
- Clock measurement methodology (CLOCK_REALTIME for cross-process)
- Dispatch table with same subsystems

So the latency difference is purely **transport mechanism**: filesystem notification
vs socket read/write.

---

## Stress Test Interpretation

The benchmark suite runs each test twice: once on a quiet system ("clean") and once
under synthetic load ("stress"). The stress phase simulates:

- **CPU pressure**: Busy loops consuming available cores
- **I/O pressure**: Continuous disk reads
- **Memory pressure**: Significant RAM allocation reducing free memory

Compare clean vs stressed p99 latencies. If stressed numbers are still within
your tolerance, your architecture is robust. If not, you may need:
- CPU affinity (`taskset`) for critical processes
- I/O priority (`ionice`) for database access
- Memory reservations (cgroups)
- Or a more deterministic IPC mechanism

---

## Common Pitfalls

1. **Running benchmarks on your desktop instead of target hardware.** Desktop SSDs,
   multiple cores, and gigabytes of RAM will give misleadingly good results.

2. **Using CLOCK_MONOTONIC for cross-process latency.** Two processes can't compare
   CLOCK_MONOTONIC timestamps meaningfully. Use CLOCK_REALTIME (zero skew on same device).

3. **Forgetting busy_timeout.** Without it, the first SQLITE_BUSY returns immediately
   as an error. With `busy_timeout=5000`, SQLite retries internally for up to 5 seconds.

4. **Testing with empty databases.** Real performance depends on table size. Pre-seed
   or run sustained tests to get realistic numbers.

5. **Ignoring WAL file growth.** WAL mode appends indefinitely unless checkpointed.
   Monitor WAL file size during sustained tests. Enable `wal_autocheckpoint` in production.
