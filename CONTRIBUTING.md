# Contributing

Thank you for your interest in improving this benchmark toolkit.

## Ground Rules

This project produces numbers that inform architecture decisions. The single most
important property of the codebase is **measurement integrity** — every latency
value must come from a real clock reading during an actual operation.

Before submitting a change, ask yourself:

1. **Would I trust this number in a design review?**
2. **Can someone trace a report cell back to raw JSON data?**
3. **Does this compile for ARM?** (`arm-linux-gnueabihf-gcc`, `GOOS=linux GOARCH=arm GOARM=7`)
4. **Did I break any existing JSON output?**

---

## What Can I Contribute?

**Welcome:**
- Bug fixes (especially measurement correctness)
- New benchmark scenarios (new write rates, reader counts, stress patterns)
- Report generator improvements (new visualizations, better comparisons)
- Documentation improvements
- Support for additional target architectures
- CI/CD pipeline setup

**Needs discussion first (open an issue):**
- New benchmark types (new transport mechanisms)
- Changes to JSON output schema (additive only — never remove fields)
- Changes to percentile algorithm or clock selection
- Changes to shared C headers or Go internal packages

---

## Development Setup

```bash
# Native build (x86 — for development and testing)
make native

# Run tests
go test ./...

# Cross-compile for ARM
docker build -t bench-build .
docker run --rm -v $(pwd):/work bench-build
```

---

## Code Conventions

### Measurement Rules

| Rule | Detail |
|---|---|
| Cross-process timestamps | `CLOCK_REALTIME` (C) or `time.Now().UnixNano()` (Go) |
| Same-process intervals | `CLOCK_MONOTONIC` (C) or `time.Since()` (Go) |
| Percentile algorithm | Floor-index on sorted ascending array: `L[floor(N * pct)]` |
| Timed scope | Wrap ONLY the system call — not prepare, bind, or data generation |
| Random seed | Fixed at `42` for reproducibility |

### JSON Output

| Rule | Detail |
|---|---|
| All numeric values | Integers only — no floats |
| Field names | `snake_case` with unit suffix: `_us`, `_ns`, `_ms`, `_bytes`, `_count` |
| Schema evolution | Additive only — never rename or remove existing fields |
| Role field | Every program outputs `"role"` as the first JSON field |

### C Code

- Compile with `-Wall -Wextra` — fix warnings at the source, no suppression
- Use `sigaction()` for signal handling (not `signal()`)
- Check every syscall return value
- Shared memory: fixed-width types only (`int64_t`, `uint32_t`) — no `int`, `long`, `bool`
- ARM: use `__ATOMIC_RELEASE` / `__ATOMIC_ACQUIRE` for cross-process shared data

### Go Code

- Check every `error` return
- Use `fmt.Fprintf(os.Stderr, ...)` + `os.Exit(1)` for fatal errors — not `log.Fatal()` or `panic()`
- Reuse packages from `benchmark/internal/` — don't duplicate stats, payload, or config logic

### Shell Scripts

- Start with `#!/bin/bash` and `set -e`
- Use `trap cleanup EXIT INT TERM` when spawning background processes
- stdout = data only, stderr = everything else

---

## Running Tests

```bash
go test ./...                        # all tests
go test ./benchmark/internal/...     # shared packages only
go test -run TestPercentile ./...    # specific test
```

All 38 tests should pass. If a change causes test failures, fix them before
submitting.

---

## Commit Messages

Use conventional-ish format:

```
fix(wal): correct busy_timeout parsing for zero value
feat(report): add complexity signal scorecard section
docs(readme): update quick start for YAML-driven workflow
```

Scope should be the benchmark or component: `wal`, `inotify`, `ipc`, `shm`,
`report`, `bench`, `common`, `internal`, `docs`.
