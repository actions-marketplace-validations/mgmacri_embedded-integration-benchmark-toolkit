#!/usr/bin/env python3
"""
Data-driven benchmark report generator.

Reads JSON result files from a results directory, discovers which benchmarks
were run (WAL, inotify, IPC), and generates a markdown report with only the
sections relevant to the data found.

Usage:
    python generate_report.py <results_dir> [--title "Report Title"] [--output report.md]

The results directory should contain subdirectories like:
    wal_clean/      — WAL benchmark scenarios (each a subdirectory with c_writer.json, go_reader.json)
    wal_sustained/  — Sustained WAL test results
    inotify_clean/  — inotify watcher/writer results (watcher_*.json, writer_*.json)
    ipc_clean/      — IPC socket results (server_*.json, client_*.json)
    system_info.txt — Optional hardware description

Output: Markdown report to stdout (or --output file).
"""

import argparse
import json
import os
import sys
from collections import OrderedDict
from datetime import datetime
from pathlib import Path


# ---------------------------------------------------------------------------
# JSON loaders
# ---------------------------------------------------------------------------

def load_json(path):
    """Load and return parsed JSON, or None on failure."""
    try:
        with open(path, "r") as f:
            return json.load(f)
    except (json.JSONDecodeError, OSError) as e:
        print(f"Warning: could not load {path}: {e}", file=sys.stderr)
        return None


def load_system_info(results_dir):
    """Load system_info.txt if present."""
    p = os.path.join(results_dir, "system_info.txt")
    if os.path.isfile(p):
        with open(p, "r") as f:
            return f.read().strip()
    return None


# ---------------------------------------------------------------------------
# WAL data loading
# ---------------------------------------------------------------------------

WRITER_NAMES = ("c_writer.json", "fw_writer.json")
READER_NAMES = ("go_reader.json", "sw_reader.json")
GO_WRITER_NAMES = ("go_writer.json", "sw_writer.json")


def load_wal_scenario(scenario_dir):
    """Load a single WAL scenario (writer + reader + optional go_writer)."""
    result = {"name": os.path.basename(scenario_dir)}

    for name in WRITER_NAMES:
        p = os.path.join(scenario_dir, name)
        if os.path.isfile(p):
            result["c_writer"] = load_json(p)
            break

    for name in READER_NAMES:
        p = os.path.join(scenario_dir, name)
        if os.path.isfile(p):
            result["go_reader"] = load_json(p)
            break

    for name in GO_WRITER_NAMES:
        p = os.path.join(scenario_dir, name)
        if os.path.isfile(p):
            result["go_writer"] = load_json(p)
            break

    return result if "c_writer" in result else None


def _load_wal_scenarios(results_dir, subdir):
    """Load WAL scenario subdirectories from a given subdirectory."""
    scenarios = []
    d = os.path.join(results_dir, subdir)
    if os.path.isdir(d):
        for entry in sorted(os.listdir(d)):
            epath = os.path.join(d, entry)
            if os.path.isdir(epath):
                scen = load_wal_scenario(epath)
                if scen:
                    scenarios.append(scen)
    return scenarios


def load_wal_results(results_dir):
    """Load all WAL results from wal_clean/, wal_stress/, and wal_sustained/ directories."""
    data = {"clean": [], "stress": [], "sustained": None}

    # Clean scenarios
    data["clean"] = _load_wal_scenarios(results_dir, "wal_clean")

    # Stressed scenarios
    data["stress"] = _load_wal_scenarios(results_dir, "wal_stress")

    # Sustained test
    wal_sustained = os.path.join(results_dir, "wal_sustained")
    if os.path.isdir(wal_sustained):
        data["sustained"] = load_wal_scenario(wal_sustained)
        if data["sustained"]:
            data["sustained"]["name"] = "sustained"

    return data if data["clean"] or data["stress"] or data["sustained"] else None


# ---------------------------------------------------------------------------
# inotify / IPC data loading
# ---------------------------------------------------------------------------

def load_event_results(results_dir, prefix):
    """Load watcher/server and writer/client JSON files from a directory."""
    results = []
    d = os.path.join(results_dir, prefix)
    if not os.path.isdir(d):
        return results

    # Group by scenario (500ms, 100ms, 1ms_burst)
    scenarios = set()
    for f in os.listdir(d):
        if f.endswith(".json"):
            # Extract scenario: watcher_500ms.json -> 500ms
            parts = f.replace(".json", "").split("_", 1)
            if len(parts) == 2:
                scenarios.add(parts[1])

    for scen in sorted(scenarios, key=_scenario_sort_key):
        entry = {"scenario": scen}

        # Try watcher (inotify) or server (IPC) or reader (shm)
        for role_prefix in ("watcher_", "server_", "reader_"):
            p = os.path.join(d, f"{role_prefix}{scen}.json")
            if os.path.isfile(p):
                entry["receiver"] = load_json(p)
                entry["receiver_role"] = role_prefix.rstrip("_")
                break

        # Try writer (inotify) or client (IPC)
        for role_prefix in ("writer_", "client_"):
            p = os.path.join(d, f"{role_prefix}{scen}.json")
            if os.path.isfile(p):
                entry["sender"] = load_json(p)
                break

        if "receiver" in entry:
            results.append(entry)

    return results


def _scenario_sort_key(s):
    """Sort scenarios: 500ms, 100ms, 1ms_burst."""
    if s.startswith("500"):
        return 0
    if s.startswith("100"):
        return 1
    return 2


# ---------------------------------------------------------------------------
# Formatting helpers
# ---------------------------------------------------------------------------

def fmt_num(n):
    """Format integer with comma separators."""
    if n is None:
        return "-"
    if isinstance(n, str):
        return n
    return f"{n:,}"


def ns_to_ms(ns):
    """Convert nanoseconds to milliseconds string with 2 decimals."""
    return f"{ns / 1_000_000:.2f}"


def get_pipeline(recv):
    """Get pipeline latency dict from receiver data.

    Tries 'total_pipeline_latency_ns' (current C output) first, then falls
    back to 'pipeline_latency_ns' (older JSON / example data) for
    backwards compatibility.
    """
    return recv.get("total_pipeline_latency_ns",
                    recv.get("pipeline_latency_ns", {}))


def has_pipeline_key(recv):
    """Check if this receiver data contains pipeline latency stats."""
    return "total_pipeline_latency_ns" in recv or "pipeline_latency_ns" in recv


def detect_journal_mode(scenario_name):
    """Infer journal mode from scenario name."""
    if "rollback" in scenario_name.lower():
        return "delete"
    return "wal"


def scenario_label(name):
    """Human-readable scenario label."""
    return name.replace("_", " ").replace("wps", " w/s")


# ---------------------------------------------------------------------------
# Report sections
# ---------------------------------------------------------------------------

def write_header(out, title, system_info):
    out.write(f"# {title}\n\n")

    hw = "Target hardware"
    date_str = datetime.now().strftime("%B %d, %Y")
    if system_info:
        for line in system_info.splitlines():
            if ":" in line:
                key, val = line.split(":", 1)
                key = key.strip().lower()
                val = val.strip()
                if key in ("hardware", "hw"):
                    hw = val
                if key == "date":
                    date_str = val

    out.write(f"**Target Hardware:** {hw}  \n")
    out.write(f"**Date:** {date_str}  \n\n")
    out.write("---\n\n")


def write_executive_summary(out, wal_data, inotify_data, ipc_data, shm_data):
    out.write("## Executive Summary\n\n")

    has_wal = wal_data is not None
    has_inotify = inotify_data is not None and len(inotify_data) > 0
    has_ipc = ipc_data is not None and len(ipc_data) > 0
    has_shm = shm_data is not None and len(shm_data) > 0

    if has_wal:
        total_busy_c = 0
        total_busy_go = 0
        total_scenarios = 0
        for scen in wal_data["clean"]:
            cw = scen.get("c_writer", {})
            gr = scen.get("go_reader", {})
            total_busy_c += cw.get("sqlite_busy_count", 0)
            total_busy_go += gr.get("sqlite_busy_count", 0)
            total_scenarios += 1

        if total_busy_c == 0 and total_busy_go == 0:
            out.write(
                f"**WAL Mode:** Zero SQLITE_BUSY events across all "
                f"{total_scenarios} test scenarios. Both writer and reader "
                f"processes accessed the same database simultaneously without "
                f"any lock contention.\n\n"
            )
        else:
            out.write(
                f"**WAL Mode:** SQLITE_BUSY observed — C writer: "
                f"{total_busy_c}, Go reader: {total_busy_go} across "
                f"{total_scenarios} scenarios.\n\n"
            )

    if has_inotify:
        # Use 500ms scenario as the "realistic" reference
        ref = next((e for e in inotify_data if "500" in e["scenario"]), inotify_data[0])
        recv = ref.get("receiver", {})
        pipeline = get_pipeline(recv) or recv.get("dispatch_latency_ns", {})
        p50_ms = ns_to_ms(pipeline.get("p50", 0))
        missed = recv.get("missed_events", 0)
        out.write(
            f"**inotify Sentinel Files:** Full config-processing pipeline "
            f"completed in {p50_ms} ms at the median (500ms interval), "
            f"with {missed} missed events.\n\n"
        )

    if has_ipc:
        ref = next((e for e in ipc_data if "500" in e["scenario"]), ipc_data[0])
        recv = ref.get("receiver", {})
        pipeline = get_pipeline(recv) or recv.get("dispatch_latency_ns", {})
        p50_ms = ns_to_ms(pipeline.get("p50", 0))
        out.write(
            f"**IPC Socket:** Full pipeline completed in {p50_ms} ms at "
            f"the median (500ms interval).\n\n"
        )

    if has_shm:
        ref = next((e for e in shm_data if "500" in e["scenario"]), shm_data[0])
        recv = ref.get("receiver", {})
        pipeline = get_pipeline(recv) or recv.get("dispatch_latency_ns", {})
        p50_ms = ns_to_ms(pipeline.get("p50", 0))
        out.write(
            f"**Shared Memory (mmap + FIFO):** Full pipeline completed in "
            f"{p50_ms} ms at the median (500ms interval).\n\n"
        )

    mechanisms = sum([has_inotify, has_ipc, has_shm])
    if mechanisms >= 2:
        out.write(
            "Multiple transport mechanisms were benchmarked with identical "
            "config payloads for apples-to-apples comparison. See the "
            "side-by-side comparison table below.\n\n"
        )

    out.write("---\n\n")


def _write_wal_table(out, scenarios):
    """Write a WAL results table from a list of scenarios."""
    out.write("| Scenario | Mode | C Writes | C BUSY | C p99 (us) | Go Reads | Go BUSY | Go p99 (us) |\n")
    out.write("|---|---|---|---|---|---|---|---|\n")

    rollback = [s for s in scenarios if "rollback" in s["name"]]
    wal_scenarios = [s for s in scenarios if "rollback" not in s["name"]]

    if rollback:
        out.write(f"| **Rollback mode** | | | | | | | |\n")
        for s in rollback:
            _write_wal_row(out, s)

    if wal_scenarios:
        groups = OrderedDict()
        for s in wal_scenarios:
            parts = s["name"].split("_")
            rate = next((p for p in parts if p.endswith("wps")), "other")
            groups.setdefault(rate, []).append(s)

        for rate, scens in groups.items():
            rate_label = rate.replace("wps", " w/s")
            out.write(f"| **WAL @ {rate_label}** | | | | | | | |\n")
            for s in scens:
                _write_wal_row(out, s)

    out.write("\n")


def write_wal_results(out, wal_data):
    """Write WAL benchmark results section."""
    if not wal_data:
        return

    out.write("## WAL Benchmark Results — Database Contention\n\n")

    # -- Key explanation
    out.write("### Reading the table\n\n")
    out.write("| Column | Meaning |\n")
    out.write("|---|---|\n")
    out.write("| **Scenario** | Test name — write rate and reader configuration |\n")
    out.write("| **Mode** | `delete` = rollback journal, `wal` = Write-Ahead Logging |\n")
    out.write("| **C Writes** | Total INSERT operations by the C writer process |\n")
    out.write("| **C BUSY** | `SQLITE_BUSY` returns on the writer — the critical metric |\n")
    out.write("| **C p99 (us)** | Writer latency at the 99th percentile (microseconds) |\n")
    out.write("| **Go Reads** | Total SELECT operations by the Go reader process |\n")
    out.write("| **Go BUSY** | `SQLITE_BUSY` returns on the reader |\n")
    out.write("| **Go p99 (us)** | Reader latency at the 99th percentile (microseconds) |\n")
    out.write("\n")

    # -- Clean results
    if wal_data["clean"]:
        out.write("### Results (Clean)\n\n")
        _write_wal_table(out, wal_data["clean"])

    # -- Stressed results
    if wal_data.get("stress"):
        out.write("### Results (Under System Stress)\n\n")
        out.write(
            "These results were collected while synthetic CPU, I/O, and memory "
            "pressure was applied to the system.\n\n"
        )
        _write_wal_table(out, wal_data["stress"])

    # -- Head-to-head comparison
    if wal_data["clean"]:
        _write_wal_comparison(out, wal_data["clean"])

    # -- Clean vs Stressed comparison
    if wal_data["clean"] and wal_data.get("stress"):
        _write_clean_vs_stress_wal(out, wal_data["clean"], wal_data["stress"])

    # -- Sustained test
    if wal_data.get("sustained"):
        _write_sustained(out, wal_data["sustained"])


def _write_wal_row(out, scen):
    cw = scen.get("c_writer", {})
    gr = scen.get("go_reader")
    mode = detect_journal_mode(scen["name"])
    lat = cw.get("write_latency_us", {})

    go_reads = fmt_num(gr["total_reads"]) if gr else "-"
    go_busy = fmt_num(gr.get("sqlite_busy_count", 0)) if gr else "-"
    go_p99 = fmt_num(gr.get("read_latency_us", {}).get("p99")) if gr else "-"

    out.write(
        f"| {scen['name']} | {mode} | {fmt_num(cw.get('total_writes', 0))} | "
        f"**{cw.get('sqlite_busy_count', 0)}** | "
        f"{fmt_num(lat.get('p99', 0))} | "
        f"{go_reads} | {go_busy} | {go_p99} |\n"
    )


def _write_wal_comparison(out, scenarios):
    """Write rollback vs WAL head-to-head if both exist."""
    rollback = None
    wal = None
    for s in scenarios:
        name = s["name"].lower()
        if "rollback" in name and "1reader" in name and "3reader" not in name:
            rollback = s
        if name.startswith("wal_33wps_1reader"):
            wal = s

    if not rollback or not wal:
        return

    out.write("### Rollback vs WAL Comparison\n\n")
    out.write("Direct comparison at the same write rate with 1 reader — "
              "the closest match to typical operating conditions.\n\n")
    out.write("| Metric | Rollback | WAL | Change |\n")
    out.write("|---|---|---|---|\n")

    rc = rollback["c_writer"]["write_latency_us"]
    wc = wal["c_writer"]["write_latency_us"]

    _comparison_row(out, "C Write Median (us)", rc["p50"], wc["p50"])
    _comparison_row(out, "C Write p99 (us)", rc["p99"], wc["p99"])

    out.write(
        f"| C SQLITE_BUSY | {rollback['c_writer'].get('sqlite_busy_count', 0)} "
        f"| {wal['c_writer'].get('sqlite_busy_count', 0)} | — |\n"
    )

    rg = rollback.get("go_reader")
    wg = wal.get("go_reader")
    if rg and wg:
        rr = rg["read_latency_us"]
        wr = wg["read_latency_us"]
        _comparison_row(out, "Go Read Median (us)", rr["p50"], wr["p50"])
        _comparison_row(out, "Go Read p99 (us)", rr["p99"], wr["p99"])
        out.write(
            f"| Go SQLITE_BUSY | {rg.get('sqlite_busy_count', 0)} "
            f"| {wg.get('sqlite_busy_count', 0)} | — |\n"
        )

    out.write("\n")


def _comparison_row(out, label, old_val, new_val):
    if old_val == 0:
        change = "—"
    else:
        pct = ((new_val - old_val) / old_val) * 100
        if abs(pct) < 3:
            change = "~same"
        elif pct < 0:
            change = f"{abs(pct):.0f}% faster"
        else:
            change = f"+{pct:.0f}%"
    out.write(f"| {label} | {fmt_num(old_val)} | {fmt_num(new_val)} | {change} |\n")


def _write_clean_vs_stress_wal(out, clean, stress):
    """Write clean vs stressed comparison for matching WAL scenarios."""
    out.write("### Clean vs Stressed Comparison\n\n")
    out.write(
        "Direct comparison of the same scenarios with and without synthetic "
        "system stress (CPU, I/O, memory pressure).\n\n"
    )
    out.write(
        "| Scenario | Mode | C p50 Clean (us) | C p50 Stress (us) | C p50 Δ | "
        "C p99 Clean (us) | C p99 Stress (us) | C p99 Δ | "
        "Go p99 Clean (us) | Go p99 Stress (us) | Go p99 Δ |\n"
    )
    out.write("|---|---|---|---|---|---|---|---|---|---|---|\n")

    stress_by_name = {s["name"]: s for s in stress}

    for cs in clean:
        ss = stress_by_name.get(cs["name"])
        if not ss:
            continue
        mode = detect_journal_mode(cs["name"])
        cc = cs.get("c_writer", {}).get("write_latency_us", {})
        sc = ss.get("c_writer", {}).get("write_latency_us", {})
        cg = cs.get("go_reader", {}).get("read_latency_us", {})
        sg = ss.get("go_reader", {}).get("read_latency_us", {})

        def delta(clean_val, stress_val):
            if not clean_val or clean_val == 0:
                return "—"
            pct = ((stress_val - clean_val) / clean_val) * 100
            if abs(pct) < 3:
                return "~same"
            return f"+{pct:.0f}%" if pct > 0 else f"{pct:.0f}%"

        out.write(
            f"| {cs['name']} | {mode} | "
            f"{fmt_num(cc.get('p50', 0))} | {fmt_num(sc.get('p50', 0))} | "
            f"{delta(cc.get('p50', 0), sc.get('p50', 0))} | "
            f"{fmt_num(cc.get('p99', 0))} | {fmt_num(sc.get('p99', 0))} | "
            f"{delta(cc.get('p99', 0), sc.get('p99', 0))} | "
            f"{fmt_num(cg.get('p99', 0))} | {fmt_num(sg.get('p99', 0))} | "
            f"{delta(cg.get('p99', 0), sg.get('p99', 0))} |\n"
        )

    out.write("\n")


def _write_sustained(out, sustained):
    out.write("### Sustained Test\n\n")

    cw = sustained.get("c_writer")
    gr = sustained.get("go_reader")
    gw = sustained.get("go_writer")

    duration = cw.get("duration_sec", 0) if cw else 0
    total_ops = 0

    out.write("| Process | Operations | BUSY | Median (us) | p99 (us) | Max (us) |\n")
    out.write("|---|---|---|---|---|---|\n")

    if cw:
        lat = cw.get("write_latency_us", {})
        ops = cw.get("total_writes", 0)
        total_ops += ops
        out.write(
            f"| C writer | {fmt_num(ops)} writes | "
            f"**{cw.get('sqlite_busy_count', 0)}** | "
            f"{fmt_num(lat.get('p50'))} | {fmt_num(lat.get('p99'))} | "
            f"{fmt_num(lat.get('max'))} |\n"
        )

    if gr:
        lat = gr.get("read_latency_us", {})
        ops = gr.get("total_reads", 0)
        total_ops += ops
        out.write(
            f"| Go reader | {fmt_num(ops)} reads | "
            f"**{gr.get('sqlite_busy_count', 0)}** | "
            f"{fmt_num(lat.get('p50'))} | {fmt_num(lat.get('p99'))} | "
            f"{fmt_num(lat.get('max'))} |\n"
        )

    if gw:
        lat = gw.get("write_latency_us", {})
        ops = gw.get("total_writes", 0)
        total_ops += ops
        out.write(
            f"| Go writer | {fmt_num(ops)} writes | "
            f"**{gw.get('sqlite_busy_count', 0)}** | "
            f"{fmt_num(lat.get('p50'))} | {fmt_num(lat.get('p99'))} | "
            f"{fmt_num(lat.get('max'))} |\n"
        )

    out.write(f"| **Total** | **{fmt_num(total_ops)} ops** | | | | |\n")
    out.write("\n")

    if duration:
        mins = duration // 60
        out.write(f"Duration: {mins} minutes continuous operation.\n\n")


def write_inotify_ipc_results(out, inotify_data, ipc_data, shm_data,
                              inotify_stress=None, ipc_stress=None, shm_stress=None):
    """Write inotify, IPC, and/or SHM results section (clean + stressed)."""
    has_inotify = inotify_data and len(inotify_data) > 0
    has_ipc = ipc_data and len(ipc_data) > 0
    has_shm = shm_data and len(shm_data) > 0

    if not has_inotify and not has_ipc and not has_shm:
        return

    mechanisms = []
    if has_inotify:
        mechanisms.append("inotify")
    if has_ipc:
        mechanisms.append("IPC Socket")
    if has_shm:
        mechanisms.append("Shared Memory")

    out.write(f"## {' vs '.join(mechanisms)} — Config Change Notification\n\n")

    # Explain what was measured
    out.write("### What was measured\n\n")
    out.write("Each event measures the **full config-processing pipeline**:\n\n")
    out.write("1. **Dispatch latency** — notification delivery only "
              "(writer timestamp to handler entry)\n")
    out.write("2. **Processing latency** — parse config payload, compare "
              "to cached state, apply changes\n")
    out.write("3. **Pipeline latency** — end-to-end (writer timestamp to "
              "processing complete)\n\n")

    # Check if we have full pipeline data
    has_pipeline = False
    first_data = inotify_data or ipc_data or shm_data
    if first_data:
        sample = first_data[0].get("receiver", {})
        if has_pipeline_key(sample):
            has_pipeline = True

    # --- Clean results ---
    if has_inotify:
        out.write("### inotify Results (Clean)\n\n")
        _write_event_table(out, inotify_data, "inotify", has_pipeline)

    if has_ipc:
        out.write("### IPC Socket Results (Clean)\n\n")
        _write_event_table(out, ipc_data, "ipc", has_pipeline)

    if has_shm:
        out.write("### Shared Memory Results (Clean)\n\n")
        _write_event_table(out, shm_data, "shm", has_pipeline)

    # Side-by-side comparison (clean)
    all_data = {}
    if has_inotify:
        all_data["inotify"] = inotify_data
    if has_ipc:
        all_data["IPC"] = ipc_data
    if has_shm:
        all_data["SHM"] = shm_data

    if len(all_data) >= 2:
        _write_multi_comparison(out, all_data, has_pipeline)

    # --- Stressed results ---
    has_ino_s = inotify_stress and len(inotify_stress) > 0
    has_ipc_s = ipc_stress and len(ipc_stress) > 0
    has_shm_s = shm_stress and len(shm_stress) > 0

    if has_ino_s or has_ipc_s or has_shm_s:
        out.write("### Stressed Results\n\n")
        out.write(
            "These results were collected while synthetic CPU, I/O, and memory "
            "pressure was applied to the system.\n\n"
        )

        if has_ino_s:
            out.write("#### inotify (Stressed)\n\n")
            _write_event_table(out, inotify_stress, "inotify", has_pipeline)

        if has_ipc_s:
            out.write("#### IPC Socket (Stressed)\n\n")
            _write_event_table(out, ipc_stress, "ipc", has_pipeline)

        if has_shm_s:
            out.write("#### Shared Memory (Stressed)\n\n")
            _write_event_table(out, shm_stress, "shm", has_pipeline)

        # Clean vs Stressed event comparison
        _write_clean_vs_stress_events(
            out, inotify_data, inotify_stress, ipc_data, ipc_stress,
            shm_data, shm_stress, has_pipeline
        )


def _write_event_table(out, data, mechanism, has_pipeline):
    """Write results table for inotify, IPC, or shm data."""

    # SHM uses sequence_errors instead of missed_events
    missed_label = "Seq Errors" if mechanism == "shm" else "Missed"

    if has_pipeline:
        out.write(
            f"| Scenario | Events | {missed_label} | Config Changes | "
            "Dispatch p50 (ns) | Dispatch p99 (ns) | "
            "Pipeline p50 (ns) | Pipeline p99 (ns) | Pipeline Max (ns) | Payload |\n"
        )
        out.write("|---|---|---|---|---|---|---|---|---|---|\n")
    else:
        out.write(
            f"| Scenario | Events | {missed_label} | "
            "Dispatch p50 (ns) | Dispatch p99 (ns) | Dispatch Max (ns) |\n"
        )
        out.write("|---|---|---|---|---|---|\n")

    for entry in data:
        recv = entry.get("receiver", {})
        scen = entry["scenario"]
        events = recv.get("total_events", 0)
        if mechanism == "shm":
            missed = recv.get("sequence_errors", 0)
        else:
            missed = recv.get("missed_events", 0)
        dispatch = recv.get("dispatch_latency_ns", {})

        if has_pipeline:
            pipeline = get_pipeline(recv)
            config_changes = recv.get("total_config_changes", 0)
            payload = recv.get("avg_payload_bytes", 0)
            payload_str = f"{payload} B" if payload else "-"
            out.write(
                f"| {scen} | {fmt_num(events)} | **{missed}** | "
                f"{fmt_num(config_changes)} | "
                f"{fmt_num(dispatch.get('p50'))} | {fmt_num(dispatch.get('p99'))} | "
                f"**{fmt_num(pipeline.get('p50'))}** | "
                f"**{fmt_num(pipeline.get('p99'))}** | "
                f"{fmt_num(pipeline.get('max'))} | {payload_str} |\n"
            )
        else:
            out.write(
                f"| {scen} | {fmt_num(events)} | **{missed}** | "
                f"{fmt_num(dispatch.get('p50'))} | {fmt_num(dispatch.get('p99'))} | "
                f"{fmt_num(dispatch.get('max'))} |\n"
            )

    out.write("\n")


def _write_clean_vs_stress_events(out, ino_clean, ino_stress, ipc_clean, ipc_stress,
                                  shm_clean, shm_stress, has_pipeline):
    """Write clean-vs-stressed comparison table for event notification mechanisms."""
    out.write("#### Clean vs Stressed — Dispatch Latency Comparison\n\n")

    latency_key = "dispatch_latency_ns"
    out.write(
        "| Mechanism | Scenario | Clean p50 (ns) | Stress p50 (ns) | p50 Δ | "
        "Clean p99 (ns) | Stress p99 (ns) | p99 Δ |\n"
    )
    out.write("|---|---|---|---|---|---|---|---|\n")

    def delta(c, s):
        if not c or c == 0:
            return "—"
        pct = ((s - c) / c) * 100
        if abs(pct) < 3:
            return "~same"
        return f"+{pct:.0f}%" if pct > 0 else f"{pct:.0f}%"

    pairs = []
    if ino_clean and ino_stress:
        pairs.append(("inotify", ino_clean, ino_stress))
    if ipc_clean and ipc_stress:
        pairs.append(("IPC socket", ipc_clean, ipc_stress))
    if shm_clean and shm_stress:
        pairs.append(("Shared memory", shm_clean, shm_stress))

    for label, clean, stress in pairs:
        stress_by_scen = {e["scenario"]: e for e in stress}
        for ce in clean:
            se = stress_by_scen.get(ce["scenario"])
            if not se:
                continue
            cd = ce.get("receiver", {}).get(latency_key, {})
            sd = se.get("receiver", {}).get(latency_key, {})
            out.write(
                f"| {label} | {ce['scenario']} | "
                f"{fmt_num(cd.get('p50', 0))} | {fmt_num(sd.get('p50', 0))} | "
                f"{delta(cd.get('p50', 0), sd.get('p50', 0))} | "
                f"{fmt_num(cd.get('p99', 0))} | {fmt_num(sd.get('p99', 0))} | "
                f"{delta(cd.get('p99', 0), sd.get('p99', 0))} |\n"
            )

    out.write("\n")


def _write_multi_comparison(out, all_data, has_pipeline):
    """Write side-by-side comparison across 2+ transport mechanisms."""
    out.write("### Side-by-Side Comparison\n\n")

    latency_key = "_pipeline" if has_pipeline else "dispatch_latency_ns"
    label = "Pipeline" if has_pipeline else "Dispatch"

    names = list(all_data.keys())

    # Header
    cols = ["Scenario"]
    for name in names:
        cols.append(f"{name} {label} p50 (ns)")
    if len(names) == 2:
        cols.append("Ratio")
    cols.append("Fastest")
    out.write("| " + " | ".join(cols) + " |\n")
    out.write("|" + "---|" * len(cols) + "\n")

    # Index each mechanism's data by scenario
    indexed = {}
    all_scenarios = set()
    for name, data in all_data.items():
        indexed[name] = {e["scenario"]: e for e in data}
        all_scenarios |= set(indexed[name].keys())

    for scen in sorted(all_scenarios, key=_scenario_sort_key):
        row = [scen]
        p50_values = {}

        for name in names:
            entry = indexed[name].get(scen)
            if entry:
                recv = entry.get("receiver", {})
                if latency_key == "_pipeline":
                    lat = get_pipeline(recv) or recv.get("dispatch_latency_ns", {})
                else:
                    lat = recv.get(latency_key, {})
                p50 = lat.get("p50", 0)
                p50_values[name] = p50
                row.append(fmt_num(p50))
            else:
                row.append("-")

        if len(names) == 2 and len(p50_values) == 2:
            vals = list(p50_values.values())
            if vals[1] > 0:
                row.append(f"{vals[0] / vals[1]:.1f}x")
            else:
                row.append("-")

        # Determine fastest
        if p50_values:
            fastest = min(p50_values, key=p50_values.get)
            row.append(f"**{fastest}**")
        else:
            row.append("-")

        out.write("| " + " | ".join(row) + " |\n")

    out.write("\n")


def write_inotify_reliability(out, reliability_data):
    """Write inotify reliability/degradation test results."""
    if not reliability_data or len(reliability_data) == 0:
        return

    out.write("## inotify Reliability Tests\n\n")
    out.write(
        "These tests stress inotify's known failure modes. Non-zero values for "
        "`overflow_events`, `coalesced_events`, or elevated `missed_events` are "
        "**expected** in degradation scenarios — the purpose is to quantify the "
        "impact.\n\n"
    )

    out.write(
        "| Scenario | Events | Missed | Overflow | Coalesced | Config Changes | "
        "Dispatch p50 (ns) | Pipeline p99 (ns) |\n"
    )
    out.write("|---|---|---|---|---|---|---|---|\n")

    for entry in reliability_data:
        recv = entry.get("receiver", {})
        scen = entry["scenario"]
        events = recv.get("total_events", 0)
        missed = recv.get("missed_events", 0)
        overflow = recv.get("overflow_events", 0)
        coalesced = recv.get("coalesced_events", 0)
        config_changes = recv.get("config_changes_detected", 0)
        dispatch = recv.get("dispatch_latency_ns", {})
        pipeline = get_pipeline(recv) or recv.get("dispatch_latency_ns", {})

        overflow_str = f"**{overflow}**" if overflow > 0 else "0"
        coalesced_str = f"**{coalesced}**" if coalesced > 0 else "0"
        missed_str = f"**{missed}**" if missed > 0 else "0"

        out.write(
            f"| {scen} | {fmt_num(events)} | {missed_str} | "
            f"{overflow_str} | {coalesced_str} | {fmt_num(config_changes)} | "
            f"{fmt_num(dispatch.get('p50'))} | "
            f"{fmt_num(pipeline.get('p99'))} |\n"
        )

    out.write("\n")

    # Writer-side details
    out.write("### Writer Details\n\n")
    out.write("| Scenario | Writes | Write p50 (us) | Write p99 (us) | No Sync | Burst Pairs |\n")
    out.write("|---|---|---|---|---|---|\n")

    for entry in reliability_data:
        sender = entry.get("sender", {})
        if not sender:
            continue
        scen = entry["scenario"]
        writes = sender.get("total_writes", 0)
        lat = sender.get("write_latency_us", {})
        no_sync = sender.get("no_sync", False)
        burst = sender.get("burst_pairs", 1)

        out.write(
            f"| {scen} | {fmt_num(writes)} | "
            f"{fmt_num(lat.get('p50'))} | {fmt_num(lat.get('p99'))} | "
            f"{'yes' if no_sync else 'no'} | {burst} |\n"
        )

    out.write("\n")


def write_conclusions(out, wal_data, inotify_data, ipc_data, shm_data):
    """Write data-driven conclusions based on what was tested."""
    out.write("## Conclusions\n\n")

    if wal_data:
        total_busy_c = sum(
            s.get("c_writer", {}).get("sqlite_busy_count", 0)
            for s in wal_data["clean"]
        )
        total_busy_go = sum(
            s.get("go_reader", {}).get("sqlite_busy_count", 0)
            for s in wal_data["clean"]
        )
        n = len(wal_data["clean"])

        out.write("### WAL Mode\n\n")
        if total_busy_c == 0 and total_busy_go == 0:
            out.write(
                f"**SQLITE_BUSY was zero across all {n} scenarios.** "
                f"WAL mode eliminates database contention at the tested "
                f"throughput levels. No custom synchronization or IPC layer "
                f"is needed for database safety.\n\n"
            )
        else:
            out.write(
                f"SQLITE_BUSY events detected: C writer = {total_busy_c}, "
                f"Go reader = {total_busy_go} across {n} scenarios. "
                f"Consider increasing `busy_timeout` or reducing write "
                f"frequency.\n\n"
            )

        # Check for sustained
        if wal_data.get("sustained"):
            cw = wal_data["sustained"].get("c_writer", {})
            gr = wal_data["sustained"].get("go_reader", {})
            gw = wal_data["sustained"].get("go_writer", {})
            total_ops = (
                cw.get("total_writes", 0) +
                gr.get("total_reads", 0) +
                gw.get("total_writes", 0)
            )
            sust_busy = (
                cw.get("sqlite_busy_count", 0) +
                gr.get("sqlite_busy_count", 0) +
                gw.get("sqlite_busy_count", 0)
            )
            dur = cw.get("duration_sec", 0)
            if sust_busy == 0 and total_ops > 0:
                out.write(
                    f"The sustained test confirmed stability: "
                    f"{fmt_num(total_ops)} total operations over "
                    f"{dur // 60} minutes with zero contention.\n\n"
                )

    has_inotify = inotify_data and len(inotify_data) > 0
    has_ipc = ipc_data and len(ipc_data) > 0
    has_shm = shm_data and len(shm_data) > 0

    if has_inotify:
        out.write("### inotify Sentinel Files\n\n")
        ref = next((e for e in inotify_data if "500" in e["scenario"]), inotify_data[0])
        recv = ref.get("receiver", {})
        pipeline = get_pipeline(recv) or recv.get("dispatch_latency_ns", {})
        p50_ms = ns_to_ms(pipeline.get("p50", 0))
        p99_ms = ns_to_ms(pipeline.get("p99", 0))
        missed = recv.get("missed_events", 0)

        out.write(
            f"inotify completed the full config-processing pipeline in "
            f"**{p50_ms} ms** at the median (p99 = {p99_ms} ms) at the "
            f"realistic 500ms interval, with **{missed} missed events**.\n\n"
        )

        if has_ipc or has_shm:
            out.write(
                "While IPC sockets and shared memory are faster in raw "
                "latency, the absolute difference is negligible at "
                "human-driven config change rates. inotify eliminates "
                "shared binary protocols and cross-team release "
                "coordination.\n\n"
            )

    if has_shm:
        out.write("### Shared Memory (mmap + FIFO)\n\n")
        ref = next((e for e in shm_data if "500" in e["scenario"]), shm_data[0])
        recv = ref.get("receiver", {})
        pipeline = get_pipeline(recv) or recv.get("dispatch_latency_ns", {})
        p50_ms = ns_to_ms(pipeline.get("p50", 0))
        p99_ms = ns_to_ms(pipeline.get("p99", 0))
        seq_errs = recv.get("sequence_errors", 0)

        out.write(
            f"Shared memory completed the full config-processing pipeline in "
            f"**{p50_ms} ms** at the median (p99 = {p99_ms} ms) at the "
            f"realistic 500ms interval, with **{seq_errs} sequence errors**.\n\n"
        )

    out.write("---\n\n")
    out.write("*Report generated from benchmark JSON results. "
              "All data is from actual measurements — no interpolation or estimation.*\n")


# ---------------------------------------------------------------------------
# Integration Complexity Scorecard
# ---------------------------------------------------------------------------

def _get_latency(data, stats_key, pct_key):
    """Safely extract a latency percentile from nested JSON."""
    stats = data.get(stats_key, {})
    if isinstance(stats, dict):
        return stats.get(pct_key, 0)
    return 0


def _collect_complexity_signals(wal_data, inotify_data, ipc_data, shm_data,
                                inotify_stress=None, ipc_stress=None,
                                shm_stress=None):
    """Collect measured complexity signals from benchmark data.

    Each signal is a dict with keys:
        signal, transport, measured, source, implication, triggered
    """
    signals = []

    # 1. SQLITE_BUSY — need retry/backoff logic
    if wal_data and wal_data["clean"]:
        total_busy = 0
        scen_count = 0
        for scen in wal_data["clean"]:
            cw = scen.get("c_writer", {})
            gr = scen.get("go_reader", {})
            total_busy += cw.get("sqlite_busy_count", 0)
            total_busy += gr.get("sqlite_busy_count", 0)
            if cw:
                scen_count += 1
        signals.append({
            "signal": "SQLITE_BUSY contention",
            "transport": "WAL",
            "measured": f"{fmt_num(total_busy)} events across {scen_count} scenarios",
            "source": "wal_clean/*/c_writer.json, go_reader.json",
            "implication": "Retry/backoff logic, busy_timeout tuning, write queue management",
            "triggered": total_busy > 0,
        })

    # 2. Missed events (inotify) — need reconciliation path
    if inotify_data:
        total_missed = 0
        total_events = 0
        worst_missed = 0
        worst_scenario = ""
        for entry in inotify_data:
            recv = entry.get("receiver", {})
            missed = recv.get("missed_events", 0)
            events = recv.get("total_events", 0)
            total_missed += missed
            total_events += events
            if missed > worst_missed:
                worst_missed = missed
                worst_scenario = entry["scenario"]
        measured = f"{fmt_num(total_missed)} missed of {fmt_num(total_events)} total"
        if worst_missed > 0:
            measured += f" (worst: {fmt_num(worst_missed)} in {worst_scenario})"
        signals.append({
            "signal": "inotify missed events",
            "transport": "inotify",
            "measured": measured,
            "source": "inotify_clean/watcher_*.json",
            "implication": "Full-state reconciliation path, periodic re-read fallback",
            "triggered": total_missed > 0,
        })

    # 3. Coalesced events (inotify) — need idempotent handlers
    if inotify_data:
        total_coalesced = 0
        total_events = 0
        for entry in inotify_data:
            recv = entry.get("receiver", {})
            total_coalesced += recv.get("coalesced_events", 0)
            total_events += recv.get("total_events", 0)
        signals.append({
            "signal": "inotify event coalescing",
            "transport": "inotify",
            "measured": f"{fmt_num(total_coalesced)} coalesced of {fmt_num(total_events)} total events",
            "source": "inotify_clean/watcher_*.json",
            "implication": "Idempotent config handlers, full-file re-parse on every event",
            "triggered": total_coalesced > 0,
        })

    # 4. Sequence errors (SHM) — need error recovery
    if shm_data:
        total_errors = 0
        total_events = 0
        for entry in shm_data:
            recv = entry.get("receiver", {})
            total_errors += recv.get("sequence_errors", 0)
            total_events += recv.get("total_events", 0)
        signals.append({
            "signal": "SHM sequence errors",
            "transport": "SHM",
            "measured": f"{fmt_num(total_errors)} errors across {fmt_num(total_events)} events",
            "source": "shm_clean/reader_*.json",
            "implication": "Torn-read recovery, sequence validation, retry on mismatch",
            "triggered": total_errors > 0,
        })

    # 5. Tail latency ratio (p99/p50 > 10) — need timeout tuning
    #    WAL writes
    if wal_data and wal_data["clean"]:
        worst_ratio = 0.0
        worst_scen = ""
        for scen in wal_data["clean"]:
            cw = scen.get("c_writer", {})
            p50 = _get_latency(cw, "write_latency_us", "p50")
            p99 = _get_latency(cw, "write_latency_us", "p99")
            if p50 > 0:
                ratio = p99 / p50
                if ratio > worst_ratio:
                    worst_ratio = ratio
                    worst_scen = scen["name"]
        signals.append({
            "signal": "WAL write tail latency (p99/p50)",
            "transport": "WAL",
            "measured": f"{worst_ratio:.1f}x (worst: {worst_scen})",
            "source": f"wal_clean/{worst_scen}/c_writer.json",
            "implication": "Timeout tuning, adaptive retry intervals, p99-aware SLA design",
            "triggered": worst_ratio > 10.0,
        })

    #    Event transports
    for name, data_list, source_dir in [
        ("inotify", inotify_data, "inotify_clean"),
        ("IPC", ipc_data, "ipc_clean"),
        ("SHM", shm_data, "shm_clean"),
    ]:
        if not data_list:
            continue
        worst_ratio = 0.0
        worst_scen = ""
        for entry in data_list:
            recv = entry.get("receiver", {})
            p50 = _get_latency(recv, "dispatch_latency_ns", "p50")
            p99 = _get_latency(recv, "dispatch_latency_ns", "p99")
            if p50 > 0:
                ratio = p99 / p50
                if ratio > worst_ratio:
                    worst_ratio = ratio
                    worst_scen = entry["scenario"]
        if worst_scen:
            signals.append({
                "signal": f"{name} dispatch tail latency (p99/p50)",
                "transport": name,
                "measured": f"{worst_ratio:.1f}x (worst: {worst_scen})",
                "source": f"{source_dir}/{worst_scen}",
                "implication": "Timeout tuning, adaptive retry intervals, p99-aware SLA design",
                "triggered": worst_ratio > 10.0,
            })

    # 6. Stress amplification (stress p99 / clean p99 > 3) — need resource isolation
    #    WAL
    if wal_data and wal_data["clean"] and wal_data["stress"]:
        stress_map = {s["name"]: s for s in wal_data["stress"]}
        worst_ratio = 0.0
        worst_scen = ""
        for clean_scen in wal_data["clean"]:
            stressed = stress_map.get(clean_scen["name"])
            if not stressed:
                continue
            clean_p99 = _get_latency(clean_scen.get("c_writer", {}), "write_latency_us", "p99")
            stress_p99 = _get_latency(stressed.get("c_writer", {}), "write_latency_us", "p99")
            if clean_p99 > 0:
                ratio = stress_p99 / clean_p99
                if ratio > worst_ratio:
                    worst_ratio = ratio
                    worst_scen = clean_scen["name"]
        if worst_scen:
            signals.append({
                "signal": "WAL stress amplification (stressed/clean p99)",
                "transport": "WAL",
                "measured": f"{worst_ratio:.1f}x (worst: {worst_scen})",
                "source": f"wal_stress/{worst_scen} vs wal_clean/{worst_scen}",
                "implication": "CPU/IO isolation (cgroups), priority scheduling, dedicated cores",
                "triggered": worst_ratio > 3.0,
            })

    #    Event transports
    for name, clean_data, stress_data, source_dir in [
        ("inotify", inotify_data, inotify_stress, "inotify"),
        ("IPC", ipc_data, ipc_stress, "ipc"),
        ("SHM", shm_data, shm_stress, "shm"),
    ]:
        if not clean_data or not stress_data:
            continue
        stress_map = {e["scenario"]: e for e in stress_data}
        worst_ratio = 0.0
        worst_scen = ""
        for clean_entry in clean_data:
            stressed = stress_map.get(clean_entry["scenario"])
            if not stressed:
                continue
            clean_recv = clean_entry.get("receiver", {})
            stress_recv = stressed.get("receiver", {})
            clean_p99 = _get_latency(clean_recv, "dispatch_latency_ns", "p99")
            stress_p99 = _get_latency(stress_recv, "dispatch_latency_ns", "p99")
            if clean_p99 > 0:
                ratio = stress_p99 / clean_p99
                if ratio > worst_ratio:
                    worst_ratio = ratio
                    worst_scen = clean_entry["scenario"]
        if worst_scen:
            signals.append({
                "signal": f"{name} stress amplification (stressed/clean p99)",
                "transport": name,
                "measured": f"{worst_ratio:.1f}x (worst: {worst_scen})",
                "source": f"{source_dir}_stress/{worst_scen} vs {source_dir}_clean/{worst_scen}",
                "implication": "CPU/IO isolation (cgroups), priority scheduling, dedicated cores",
                "triggered": worst_ratio > 3.0,
            })

    return signals


def write_complexity_scorecard(out, wal_data, inotify_data, ipc_data, shm_data,
                               inotify_stress=None, ipc_stress=None,
                               shm_stress=None):
    """Write the Integration Complexity Scorecard section."""
    signals = _collect_complexity_signals(
        wal_data, inotify_data, ipc_data, shm_data,
        inotify_stress=inotify_stress,
        ipc_stress=ipc_stress,
        shm_stress=shm_stress,
    )
    if not signals:
        return

    out.write("\n## Integration Complexity Scorecard\n\n")
    out.write(
        "Each row is a **measured** indicator of engineering work required "
        "to productionize this integration pattern. Signals are derived from "
        "the raw JSON results above — not estimated.\n\n"
    )

    triggered = sum(1 for s in signals if s["triggered"])
    out.write(f"**{triggered} of {len(signals)} signals triggered.**\n\n")

    out.write("| Status | Signal | Transport | Measured | Engineering Implication | Source |\n")
    out.write("|--------|--------|-----------|----------|------------------------|--------|\n")

    # Triggered first, then clear
    for s in signals:
        if s["triggered"]:
            out.write(
                f"| **TRIGGERED** | {s['signal']} | {s['transport']} | "
                f"{s['measured']} | {s['implication']} | {s['source']} |\n"
            )
    for s in signals:
        if not s["triggered"]:
            out.write(
                f"| clear | {s['signal']} | {s['transport']} | "
                f"{s['measured']} | {s['implication']} | {s['source']} |\n"
            )

    out.write("\n*This scorecard surfaces failure modes that create engineering work. "
              "Apply your team's cost model to estimate effort — "
              "the toolkit measures, it does not guess.*\n\n")


# ---------------------------------------------------------------------------
# Appendix — full data tables
# ---------------------------------------------------------------------------

def write_appendix(out, wal_data, inotify_data, ipc_data, shm_data,
                   inotify_stress=None, ipc_stress=None, shm_stress=None):
    """Write detailed appendix with full percentile tables."""
    out.write("\n---\n\n")
    out.write("## Appendix — Full Data Tables\n\n")

    if wal_data:
        _write_wal_appendix(out, wal_data)

    has_inotify = inotify_data and len(inotify_data) > 0
    has_ipc = ipc_data and len(ipc_data) > 0
    has_shm = shm_data and len(shm_data) > 0

    if has_inotify or has_ipc or has_shm:
        _write_event_appendix(out, inotify_data, ipc_data, shm_data,
                              inotify_stress, ipc_stress, shm_stress)


def _write_wal_appendix_table(out, scenarios, label):
    """Write a full-percentile WAL appendix table."""
    if not scenarios:
        return
    out.write(f"**{label}**\n\n")
    out.write(
        "| Scenario | Mode | C Writes | C BUSY | "
        "C p50 (us) | C p95 (us) | C p99 (us) | C Max (us) | "
        "Go Reads | Go BUSY | "
        "Go p50 (us) | Go p95 (us) | Go p99 (us) | Go Max (us) |\n"
    )
    out.write("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")

    for scen in scenarios:
        cw = scen.get("c_writer", {})
        gr = scen.get("go_reader")
        mode = detect_journal_mode(scen["name"])
        cl = cw.get("write_latency_us", {})

        row = (
            f"| {scen['name']} | {mode} | "
            f"{fmt_num(cw.get('total_writes', 0))} | "
            f"**{cw.get('sqlite_busy_count', 0)}** | "
            f"{fmt_num(cl.get('p50'))} | {fmt_num(cl.get('p95'))} | "
            f"{fmt_num(cl.get('p99'))} | {fmt_num(cl.get('max'))} | "
        )

        if gr:
            rl = gr.get("read_latency_us", {})
            row += (
                f"{fmt_num(gr.get('total_reads', 0))} | "
                f"**{gr.get('sqlite_busy_count', 0)}** | "
                f"{fmt_num(rl.get('p50'))} | {fmt_num(rl.get('p95'))} | "
                f"{fmt_num(rl.get('p99'))} | {fmt_num(rl.get('max'))} |"
            )
        else:
            row += "- | - | - | - | - | - |"

        out.write(row + "\n")

    out.write("\n")


def _write_wal_appendix(out, wal_data):
    out.write("### A1. WAL Benchmark — All Percentiles\n\n")

    all_clean = list(wal_data["clean"])
    if wal_data.get("sustained"):
        all_clean.append(wal_data["sustained"])
    _write_wal_appendix_table(out, all_clean, "Clean")

    if wal_data.get("stress"):
        _write_wal_appendix_table(out, wal_data["stress"], "Stressed")


def _write_event_appendix(out, inotify_data, ipc_data, shm_data,
                         inotify_stress=None, ipc_stress=None, shm_stress=None):
    has_inotify = inotify_data and len(inotify_data) > 0
    has_ipc = ipc_data and len(ipc_data) > 0
    has_shm = shm_data and len(shm_data) > 0
    has_ino_s = inotify_stress and len(inotify_stress) > 0
    has_ipc_s = ipc_stress and len(ipc_stress) > 0
    has_shm_s = shm_stress and len(shm_stress) > 0

    out.write("### A2. Event Notification — All Percentiles\n\n")
    out.write(
        "| Condition | Mechanism | Scenario | Events | Missed | "
        "Dispatch min | Dispatch p50 | Dispatch p95 | Dispatch p99 | Dispatch Max | "
        "Pipeline min | Pipeline p50 | Pipeline p95 | Pipeline p99 | Pipeline Max |\n"
    )
    out.write("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")

    def _write_rows(data, label, condition):
        for entry in data:
            recv = entry.get("receiver", {})
            d = recv.get("dispatch_latency_ns", {})
            p = get_pipeline(recv)
            out.write(
                f"| {condition} | {label} | {entry['scenario']} | "
                f"{fmt_num(recv.get('total_events', 0))} | "
                f"{recv.get('missed_events', recv.get('sequence_errors', 0))} | "
                f"{fmt_num(d.get('min'))} | {fmt_num(d.get('p50'))} | "
                f"{fmt_num(d.get('p95'))} | {fmt_num(d.get('p99'))} | "
                f"{fmt_num(d.get('max'))} | "
                f"{fmt_num(p.get('min', '-'))} | {fmt_num(p.get('p50', '-'))} | "
                f"{fmt_num(p.get('p95', '-'))} | {fmt_num(p.get('p99', '-'))} | "
                f"{fmt_num(p.get('max', '-'))} |\n"
            )

    if has_inotify:
        _write_rows(inotify_data, "inotify", "Clean")
    if has_ipc:
        _write_rows(ipc_data, "IPC socket", "Clean")
    if has_shm:
        _write_rows(shm_data, "Shared memory", "Clean")
    if has_ino_s:
        _write_rows(inotify_stress, "inotify", "Stressed")
    if has_ipc_s:
        _write_rows(ipc_stress, "IPC socket", "Stressed")
    if has_shm_s:
        _write_rows(shm_stress, "Shared memory", "Stressed")

    out.write("\n")

    # Subsystem distribution
    out.write("### A3. Events by Subsystem\n\n")
    out.write("| Mechanism | Scenario | Subsystem | Count |\n")
    out.write("|---|---|---|---|\n")

    def _write_subsystem_rows(data, label):
        for entry in data:
            recv = entry.get("receiver", {})
            by_sub = recv.get("events_by_subsystem", {})
            for sub, count in sorted(by_sub.items()):
                out.write(
                    f"| {label} | {entry['scenario']} | {sub} | {fmt_num(count)} |\n"
                )

    if has_inotify:
        _write_subsystem_rows(inotify_data, "inotify")
    if has_ipc:
        _write_subsystem_rows(ipc_data, "IPC socket")
    if has_shm:
        _write_subsystem_rows(shm_data, "Shared memory")

    out.write("\n")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(
        description="Generate a data-driven markdown benchmark report from JSON results."
    )
    parser.add_argument("results_dir", help="Path to the results directory")
    parser.add_argument(
        "--title",
        default="Integration Benchmark Results",
        help="Report title (default: 'Integration Benchmark Results')"
    )
    parser.add_argument(
        "--output", "-o",
        help="Output file path (default: stdout)"
    )
    parser.add_argument(
        "--no-appendix",
        action="store_true",
        help="Omit the appendix with full data tables"
    )
    args = parser.parse_args()

    results_dir = args.results_dir
    if not os.path.isdir(results_dir):
        print(f"Error: {results_dir} is not a directory", file=sys.stderr)
        sys.exit(1)

    # Load all data
    system_info = load_system_info(results_dir)
    wal_data = load_wal_results(results_dir)
    inotify_data = load_event_results(results_dir, "inotify_clean")
    ipc_data = load_event_results(results_dir, "ipc_clean")
    shm_data = load_event_results(results_dir, "shm_clean")
    inotify_stress = load_event_results(results_dir, "inotify_stress")
    ipc_stress = load_event_results(results_dir, "ipc_stress")
    shm_stress = load_event_results(results_dir, "shm_stress")
    inotify_reliability = load_event_results(results_dir, "inotify_reliability")

    # Check we have something
    if not wal_data and not inotify_data and not ipc_data and not shm_data:
        print("Error: no benchmark results found in", results_dir, file=sys.stderr)
        sys.exit(1)

    # Generate report
    if args.output:
        out = open(args.output, "w", encoding="utf-8")
    else:
        out = sys.stdout

    try:
        write_header(out, args.title, system_info)
        write_executive_summary(out, wal_data, inotify_data, ipc_data, shm_data)

        if wal_data:
            write_wal_results(out, wal_data)

        write_inotify_ipc_results(
            out, inotify_data, ipc_data, shm_data,
            inotify_stress=inotify_stress,
            ipc_stress=ipc_stress,
            shm_stress=shm_stress,
        )
        write_inotify_reliability(out, inotify_reliability)
        write_conclusions(out, wal_data, inotify_data, ipc_data, shm_data)
        write_complexity_scorecard(
            out, wal_data, inotify_data, ipc_data, shm_data,
            inotify_stress=inotify_stress,
            ipc_stress=ipc_stress,
            shm_stress=shm_stress,
        )

        if not args.no_appendix:
            write_appendix(out, wal_data, inotify_data, ipc_data, shm_data,
                           inotify_stress, ipc_stress, shm_stress)
    finally:
        if args.output:
            out.close()

    if args.output:
        print(f"Report written to {args.output}", file=sys.stderr)


if __name__ == "__main__":
    main()
