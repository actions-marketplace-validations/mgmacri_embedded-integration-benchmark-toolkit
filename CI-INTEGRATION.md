# CI Integration Guide

This document covers two ways to integrate the benchmark toolkit into your
CI pipeline: as a **reusable GitHub Action** or as a **Docker-based step**
for any CI system.

The toolkit serves two CI purposes:

1. **Performance gating** — `bench report --gate` exits non-zero when any
   measured value exceeds your configured thresholds.
2. **Threat model triggering** — the report detects which IPC transports
   your system uses and maps each to its attack surface. When a new transport
   appears, your pipeline can flag it for security review.

---

## Quick Start: GitHub Action

Add the toolkit as a step in any workflow. The action builds the toolkit,
generates the report, and gates on your thresholds.

### Report-only mode (pre-collected results checked into your repo)

```yaml
- name: Benchmark gate
  uses: mgmacri/ipc-you-might-not-need@v1
  with:
    results: path/to/results/
    config: bench.yaml
    gate: 'true'
```

### Full run mode (target device accessible from runner)

```yaml
- name: Run benchmarks and gate
  uses: mgmacri/ipc-you-might-not-need@v1
  with:
    config: bench.yaml
    target-ip: ${{ secrets.BENCH_TARGET_IP }}
    duration: '30'
    gate: 'true'
```

### Action inputs

| Input | Default | Description |
|-------|---------|-------------|
| `config` | `bench.yaml` | Path to bench.yaml with thresholds |
| `results` | *(empty)* | Path to pre-collected results. If empty, builds and runs live |
| `duration` | `30` | Seconds per benchmark scenario |
| `only` | *(empty)* | Run only: `wal`, `events`, `sustained` |
| `gate` | `true` | Exit non-zero if any threshold is exceeded |
| `report-output` | `BENCHMARK-REPORT.md` | Where to write the report |
| `target-ip` | *(empty)* | ARM target device IP (required for live runs) |

### Action outputs

| Output | Description |
|--------|-------------|
| `verdict` | `pass` or `fail` |
| `report-path` | Path to the generated report file |
| `transports` | Comma-separated list of detected IPC transports |
| `transport-count` | Number of transports requiring threat model coverage |

The report is automatically uploaded as a workflow artifact.

---

## Docker-Based Integration (Any CI System)

The Docker image cross-compiles all binaries. After that, `bench report --gate`
evaluates thresholds and returns a non-zero exit code on failure.

### 1. Build the image

```bash
docker build -t bench-build .
docker run --rm -v "$PWD":/work bench-build
```

### 2. Deploy and run (if you have a target device)

```bash
./deploy.sh 192.168.1.100
ssh root@192.168.1.100 "cd /tmp/benchmark && ./run_all.sh 60"
./collect_results.sh 192.168.1.100
```

### 3. Generate report with CI gating

```bash
# Exits 0 on pass, 1 on fail
./build/bench report results/ --config bench.yaml --gate
```

### GitLab CI example

```yaml
benchmark-gate:
  image: docker:latest
  services:
    - docker:dind
  script:
    - docker build -t bench-build .
    - docker run --rm -v "$PWD":/work bench-build
    # Assuming results are pre-collected or available via artifact
    - ./build/bench report results/ --config bench.yaml --gate --output report.md
  artifacts:
    paths:
      - report.md
    when: always
```

### Jenkins pipeline example

```groovy
stage('Benchmark Gate') {
    steps {
        sh 'docker build -t bench-build .'
        sh 'docker run --rm -v "$PWD":/work bench-build'
        sh './build/bench report results/ --config bench.yaml --gate --output report.md'
    }
    post {
        always {
            archiveArtifacts artifacts: 'report.md'
        }
    }
}
```

---

## Configuring Thresholds

Thresholds live in `bench.yaml`. The `--gate` flag compares every measured
value against these limits and fails the pipeline if any exceed them.

```yaml
# bench.yaml — thresholds that gate your CI pipeline
thresholds:
  wal:
    max_busy_pct: 1.0              # SQLITE_BUSY rate (%)
    max_p99_write_latency_us: 5000 # p99 write latency (µs)
    max_p99_read_latency_us: 5000  # p99 read latency (µs)
  events:
    max_p99_dispatch_latency_us: 5000  # p99 event dispatch (µs)
    max_missed_events_pct: 0.0         # missed inotify events (%)
  sustained:
    max_busy_pct: 1.0
    max_p99_write_latency_us: 10000
```

Start with generous thresholds and tighten them as your system stabilizes.
Use the [EXAMPLE-REPORT.md](example/EXAMPLE-REPORT.md) to see what real
measurements look like.

---

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | All verdicts within thresholds |
| `1` | One or more verdicts exceeded thresholds |

The report is always written (to stdout or `--output` file) regardless of
exit code. Inspect the verdict table in the report to see exactly which
criteria failed and by how much.

---

## Workflow Patterns

### Performance regression gate (recommended)

Check in your benchmark results alongside code. On every PR, re-run
benchmarks and compare against thresholds. The `--gate` flag makes the
pipeline fail-fast on regressions.

```yaml
on: pull_request
jobs:
  perf-gate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: mgmacri/ipc-you-might-not-need@v1
        with:
          results: results/baseline/
          config: bench.yaml
          gate: 'true'
```

### Threat model review trigger

When your project adds a new IPC transport (e.g., switching from inotify
to Unix sockets), the report automatically detects it. Use the `transports`
and `transport-count` outputs to trigger a security review.

Each transport is a new **interface** in your system's threat model — it
has a trust boundary, attack surface, and data flow that need STRIDE
analysis (or equivalent).

```yaml
on: pull_request
jobs:
  architecture-review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Run benchmark analysis
        id: bench
        uses: mgmacri/ipc-you-might-not-need@v1
        with:
          results: results/baseline/
          config: bench.yaml
          gate: 'false'    # don't fail on perf — just detect transports

      - name: Check for new interfaces
        run: |
          echo "Detected transports: ${{ steps.bench.outputs.transports }}"
          echo "Count: ${{ steps.bench.outputs.transport-count }}"

          # Compare against known-reviewed transports
          # If count increases, flag for threat model update
          KNOWN_COUNT=2  # update this when threat model is reviewed
          ACTUAL=${{ steps.bench.outputs.transport-count }}
          if [ "$ACTUAL" -gt "$KNOWN_COUNT" ]; then
            echo "::error::New transport interface detected — threat model review required"
            echo "Review the 'Threat Model Surface Area' section in the benchmark report"
            exit 1
          fi
```

The generated report includes a **Threat Model Surface Area** section that
maps each detected transport to:

| Column | What it tells you |
|--------|-------------------|
| **Interface** | What the IPC mechanism actually is (socket, file, shm segment) |
| **Trust Boundary** | Who can access it (same-user, same-host, DAC-controlled) |
| **Attack Vector** | Known attack patterns (symlink race, injection, FD exhaustion) |
| **Data Flow** | Direction and participants (writer → medium → reader) |

This table is designed as direct input to STRIDE analysis. When a new row
appears, it means your system has a new interface that doesn't yet exist
in your threat model.

### Nightly benchmark with artifact collection

Run full benchmarks nightly, upload the report, but don't gate (use for
trend tracking rather than blocking).

```yaml
on:
  schedule:
    - cron: '0 3 * * *'
jobs:
  nightly-bench:
    runs-on: [self-hosted, arm]
    steps:
      - uses: actions/checkout@v4
      - uses: mgmacri/ipc-you-might-not-need@v1
        with:
          config: bench.yaml
          target-ip: 192.168.1.100
          duration: '120'
          gate: 'false'
```

---

*See also: [ARCHITECTURE.md](ARCHITECTURE.md) · [INTEGRATION-GUIDE.md](INTEGRATION-GUIDE.md) · [THROUGHPUT-GUIDE.md](THROUGHPUT-GUIDE.md)*
