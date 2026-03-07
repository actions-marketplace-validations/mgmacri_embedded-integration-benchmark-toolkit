# Throughput Scaling Guide — When to Graduate from inotify

> Part of [IPC You Might Not Need](README.md). See also:
> [Integration Guide](INTEGRATION-GUIDE.md) | [Measurement Methodology](ARCHITECTURE.md)

inotify sentinel files are the simplest config notification mechanism in this
toolkit. But simplicity has a throughput ceiling. This guide maps that ceiling
with real numbers and tells you exactly when — and how — to move to the next
transport.

**If your notification path is on a hot loop that can't be decoupled from the
critical path, start here.** The throughput buckets below tell you which
transport gives you deterministic behavior at your rate.

> **Security implication of every transport upgrade:** Each step up this
> spectrum — from inotify to sockets to shared memory — adds IPC interfaces
> with progressively larger attack surfaces. inotify's security surface
> (directory permissions, file format) largely overlaps with your existing
> filesystem threat model. Sockets and shared memory introduce **new**
> trust boundaries that don't exist in a filesystem-only architecture.
> The benchmark data tells you *when* you're forced to graduate. The
> [CI integration](CI-INTEGRATION.md) detects *that* you graduated and
> flags it for security review. This is by design — the toolkit acts as
> a **security canary**: when a threshold is exceeded, the transport
> upgrade it triggers is your signal to circle back and build a threat
> model for the new interface.

---

## The Throughput Spectrum

Every transport in this toolkit was measured with identical payloads (~200 B),
identical config parsing, and identical clock methodology on the same Cortex-A7
hardware. The only variable is the delivery mechanism.

```
         inotify                    sockets                  shared memory
         ◄──────────────────────────────────────────────────────────────────►
         simplest                                                    fastest

  ≤ 2/sec    ≤ 10/sec    ≤ 100/sec    ≤ 1000/sec    > 1000/sec
  ┌─────────┬───────────┬────────────┬─────────────┬──────────────┐
  │  GREEN  │   GREEN   │   YELLOW   │    RED      │   BLOCKED    │
  │ inotify │  inotify  │  inotify*  │  sockets    │  shm / ring  │
  │         │           │  or socket │             │   buffer     │
  └─────────┴───────────┴────────────┴─────────────┴──────────────┘
                                 ▲
                        inotify ceiling
                   (coalescing + overflow begin)
```

\* At 100/sec inotify still works on our hardware with zero missed events, but
the margin is thin. One I/O storm or CPU spike pushes it into non-deterministic
territory.

---

## Bucket 1: ≤ 10 notifications/sec — Use inotify

**Measured behavior (Cortex-A7):**

| Metric | Value |
|---|---|
| Dispatch p50 | 791 µs |
| Dispatch p99 | 1,391 µs |
| Pipeline p50 | 921 µs |
| Pipeline p99 | 1,521 µs |
| Missed events | **0** |
| Coalesced events | **0** |

**Why it works:** At ≤ 10/sec, each notification has ~100 ms of quiet time
between events. The kernel inotify queue never fills. Atomic rename guarantees
the watcher sees a complete file. The filesystem acts as a natural buffer — if
the watcher restarts, the last state is still on disk.

**When this is your hot path:** If your critical loop runs at ≤ 10 Hz and needs
to react to config changes, inotify adds < 1 ms p50 to the loop iteration.
That's negligible for control loops running at 10 Hz (100 ms budget per tick).

**Architecture:**
```
Config source ──atomic rename──→ /tmp/watch_dir/subsystem
                                         │
                                 inotify IN_MOVED_TO
                                         ↓
Hot loop ──reads file──→ apply config (< 1 ms overhead)
```

**Recommendation:** Use inotify. The complexity savings are significant and the
latency is well within budget. inotify's attack surface — directory permissions,
file format convention — overlaps with your existing filesystem threat model,
so the incremental security review cost is minimal compared to adding a new
socket or shared memory interface.

---

## Bucket 2: 10–100 notifications/sec — inotify works, but verify

**Measured behavior (Cortex-A7, 100 ms interval = 10/sec):**

| Metric | Value |
|---|---|
| Dispatch p50 | 802 µs |
| Dispatch p99 | 1,208 µs |
| Pipeline p50 | 941 µs |
| Pipeline p99 | 1,348 µs |
| Missed events | **0** |
| Coalesced events | **0** |

At 10/sec on clean hardware, inotify is still deterministic. But this is the
**transition zone** — your margin depends on what else is happening on the
system.

### What erodes the margin

| Stressor | Effect on inotify |
|---|---|
| **CPU saturation** | Watcher process gets fewer scheduler ticks → events queue up |
| **I/O storm** (eMMC, SD card) | VFS lock contention delays rename visibility |
| **Large payloads** (> 1 KB) | File read time grows → processing overlaps with next event |
| **Burst patterns** | 50 events in 100 ms then silence → queue pressure during burst |

### Decision criteria at this rate

Run the **inotify reliability suite** (`run_inotify_reliability.sh`) at your
rate and check three fields:

```
missed_events       → must be 0
overflow_events     → must be 0
coalesced_events    → should be 0 (non-zero means the kernel merged events)
```

If all three are zero under your worst-case system load (stress test phase),
inotify is safe. If any are non-zero, you're at the edge — move to sockets
now, before the problem surfaces in production.

**Recommendation:** Use inotify if reliability tests pass under stress. If
they don't, skip to Bucket 3.

---

## Bucket 3: 100–1,000 notifications/sec — Use sockets

**Measured behavior (Cortex-A7, 1 ms burst = ~1,000/sec):**

| Metric | inotify | IPC socket | shm (mmap+FIFO) |
|---|---|---|---|
| Events delivered | 52,102 | 55,201 | — |
| Dispatch p50 | 618 µs | 312 µs | — |
| Dispatch p99 | 1,991 µs | 698 µs | — |
| Pipeline p99 | 2,162 µs | 761 µs | — |
| Pipeline max | **22,301 µs** | 3,921 µs | — |
| Missed events | **271** | **0** | **0** |

At 1,000/sec, inotify is **non-deterministic**:
- 271 missed events (0.5% loss rate)
- 22 ms tail latency (max) — a 15x spike over p99
- Pipeline p99 at 2.1 ms and climbing

Meanwhile sockets deliver every event, with p99 under 800 µs and a max
under 4 ms.

### Why inotify breaks down here

1. **Event coalescing.** If the writer renames `sensor_calibration` twice
   before the watcher reads, the kernel delivers one `IN_MOVED_TO` — the
   first update is silently lost.

2. **Queue overflow.** The kernel inotify queue (default 16,384 events) fills
   when events arrive faster than the watcher can drain them. Once
   `IN_Q_OVERFLOW` fires, the watcher has **no way to know which files
   changed** — it must re-scan the entire directory.

3. **Filesystem overhead.** Each notification requires: temp file create →
   write → fsync → rename → inotify event → file open → read → close.
   Sockets skip the filesystem entirely: write bytes → read bytes.

### The hot-path problem

If your critical loop runs at 100–1,000 Hz and config changes arrive at the
same rate, you cannot decouple notification from processing. Every iteration
must check for updates. At this rate:

- **inotify** forces a filesystem round-trip per check (even `stat()` has VFS
  overhead on a loaded system)
- **Sockets** give you a non-blocking `recv()` — check for data, get it
  immediately if present, move on if not. Zero filesystem involvement.
- **Shared memory** gives you an atomic load — one instruction, no syscall

**Architecture (sockets):**
```
Config source ──write──→ Unix domain socket
                                │
                         non-blocking recv()
                                ↓
Hot loop: if (data available) { parse + apply } else { continue }
```

**Recommendation:** Use Unix domain sockets. The complexity cost (socket
lifecycle, reconnection, framing) is justified by guaranteed delivery and 2–3x
lower latency.

> **Threat model trigger:** Moving from inotify to sockets adds a Unix domain
> socket interface — a new trust boundary with its own attack surface (symlink
> races, FD exhaustion, message injection). When the CI pipeline detects this
> new transport, it flags the change for security review. Design a threat
> model entry for the socket interface before shipping. See
> [CI-INTEGRATION.md](CI-INTEGRATION.md#threat-model-review-trigger).

---

## Bucket 4: > 1,000 notifications/sec — Use shared memory

At > 1,000/sec, even socket syscalls start to matter. Each `send()`/`recv()`
is a context switch through the kernel. Shared memory eliminates that:

- Writer stores payload in `mmap`'d region + atomic release
- Reader checks via atomic acquire — one instruction, no syscall
- FIFO byte signals "new data available" (or poll the ready flag)

### When you need this

- **Control loops at 1 kHz+** that react to parameter changes
- **Sensor fusion** where calibration updates arrive per-sample
- **Real-time audio/video** pipelines with per-frame metadata

### The tradeoff

| | Sockets | Shared Memory |
|---|---|---|
| Latency | ~300–700 µs | ~50–200 µs |
| Delivery guarantee | Yes (kernel buffers) | Manual (sequence tracking) |
| Complexity | Medium | High |
| Memory ordering | Not your problem | **Your problem** (ARM barriers) |
| Crash recovery | Kernel cleans up socket | Must `shm_unlink` manually |
| Debugging | `ss`, `strace`, wireshark | `hexdump /dev/shm/...` |

Shared memory on ARMv7 requires explicit memory barriers. Without
`__ATOMIC_RELEASE` on writes and `__ATOMIC_ACQUIRE` on reads, the reader
can see `ready != 0` but read **stale payload data**. This bug is invisible
on x86 (TSO memory model masks it) and only manifests on ARM under load.

**Architecture (shared memory):**
```
Config source ──mmap write + atomic_store(ready)──→ /dev/shm/config
                                                         │
                           atomic_load(ready) ← polling or FIFO signal
                                    ↓
Hot loop: memcpy local copy → parse + apply (zero-copy read)
```

**Recommendation:** Use shared memory only when you've measured that socket
latency is insufficient. The ARM memory ordering complexity is a real source
of subtle bugs. Benchmark first, then decide.

> **Threat model trigger:** Shared memory adds the largest interface surface
> of any transport in this toolkit — byte-level struct coupling, memory
> ordering assumptions, and a signaling path with no kernel-mediated access
> control beyond segment permissions. A compromised writer can inject
> arbitrary data that the reader trusts implicitly. This interface
> **requires** a dedicated threat model entry with STRIDE analysis before
> shipping. See [CI-INTEGRATION.md](CI-INTEGRATION.md#threat-model-review-trigger).

---

## Decision Flowchart

```
                    What is your notification rate?
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
          ≤ 10/sec       10–100/sec       > 100/sec
              │               │               │
              │         Run reliability       │
              │           tests under         │
              │             stress            │
              │          ┌────┴────┐          │
              │       pass?      fail?        │
              │          │         │           │
              ▼          ▼         ▼           ▼
           inotify    inotify   sockets    ┌──┴──┐
                                       100–1K  > 1K
                                         │       │
                                         ▼       ▼
                                      sockets   shm
```

### Can the hot path be decoupled?

Before choosing a faster transport, ask whether the notification path can be
moved **off** the critical loop:

```
Option A: Decouple (preferred)
  Background thread ──watches for config──→ atomic swap of config pointer
  Hot loop ──reads pointer──→ uses current config (no I/O on hot path)

Option B: Inline (when you can't decouple)
  Hot loop ──non-blocking check──→ socket recv() or atomic_load(shm)
```

Option A works at **any** throughput because the hot loop never does I/O — it
just reads a pointer. The background thread absorbs notification latency. This
is the optimal pattern when your hot path and your notification rate are
independent.

Option B is required when the config change **must** take effect on the very
next iteration — for example, a safety interlock that must be honored within
one control cycle. In this case, the transport must be faster than your loop
period.

| Loop frequency | Max notification latency | Recommended transport |
|---|---|---|
| 10 Hz (100 ms) | 10 ms | inotify (even with decoupling overhead) |
| 100 Hz (10 ms) | 1 ms | Sockets (non-blocking recv) |
| 1 kHz (1 ms) | 100 µs | Shared memory (atomic load) |
| 10 kHz (100 µs) | 10 µs | Shared memory + busy-poll (no FIFO) |

---

## Scaling inotify Before Abandoning It

If you're in the 10–100/sec range and want to push inotify further before
switching transports, these techniques extend the ceiling:

### 1. Batch updates

Instead of one rename per field change, collect changes over a window and
write them as a single file. This reduces notification rate by the batch
factor.

```
Before: 50 field changes/sec → 50 renames/sec → 50 inotify events/sec
After:  50 field changes/sec → batch into 5 renames/sec → 5 events/sec
```

### 2. Increase kernel queue size

```bash
echo 65536 > /proc/sys/fs/inotify/max_queued_events  # default: 16384
```

This buys headroom for bursts but doesn't solve coalescing — if the same
filename is renamed twice before the watcher reads, you still lose the
first event regardless of queue size.

### 3. Use unique filenames

```
sensor_calibration.00001
sensor_calibration.00002
sensor_calibration.00003
```

Each rename creates a new file, so the kernel delivers separate events.
The watcher must now manage cleanup (delete processed files) and ordering
(sort by sequence number). This adds complexity that approaches socket
complexity — at that point, use sockets.

### 4. Move to tmpfs

If you're on eMMC or SD card, the filesystem I/O is your bottleneck. Move the
watch directory to tmpfs:

```bash
mount -t tmpfs -o size=10M tmpfs /tmp/watch_dir
```

This eliminates storage I/O entirely. All benchmark results in this toolkit
were measured on tmpfs — if your production system uses block storage, expect
worse numbers.

---

## Summary Table

| Rate | Transport | p50 Latency | p99 Latency | Missed Events | Complexity | People-Hours |
|---|---|---|---|---|---|---|
| ≤ 10/sec | inotify | ~900 µs | ~1,500 µs | 0 | Lowest | ~2 days |
| 10–100/sec | inotify (verified) | ~900 µs | ~1,300 µs | 0 (verify) | Low | ~3 days |
| 100–1,000/sec | Unix sockets | ~350 µs | ~750 µs | 0 | Medium | ~1–2 weeks |
| > 1,000/sec | Shared memory | ~100 µs | ~300 µs | 0 (manual) | High | ~2–3 weeks |

All latency values are from a Cortex-A7 (STM32MP157) on tmpfs. Your hardware
will differ — run the benchmarks on your target and fill in your own numbers.
The bucket boundaries shift with CPU speed, but the relative ordering and
the failure modes remain the same.

---

## The Security Cost of Graduation

Every row in the table above is also a row in your threat model. The toolkit's
CI integration is designed around this principle:

1. **Benchmark data shows your current transport can't meet latency requirements**
   — inotify p99 exceeds your threshold, or missed events appear under load.
2. **You're forced to adopt a tighter-coupled transport** — sockets or shared memory.
3. **The CI pipeline detects the new transport** via the `transport-count` output
   and flags it for review.
4. **Your team designs a threat model entry** for the new interface — trust boundary,
   attack vectors, data flows, mitigations — before the change ships.

This is the core workflow the toolkit enables: **the benchmark is the canary, the
CI gate is the tripwire, and the threat model is the required response.** The goal
is never to have an IPC interface in production that hasn't been through a
security design review.

See [CI-INTEGRATION.md](CI-INTEGRATION.md) for the GitHub Action configuration
that implements this workflow.
