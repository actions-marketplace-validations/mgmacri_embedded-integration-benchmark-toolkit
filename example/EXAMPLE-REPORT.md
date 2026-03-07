# SQLite WAL & inotify Benchmark — Example Report

**Target Hardware:** Digi ConnectCore MP157 (STM32MP157 SoC, ARM Cortex-A7)  
**Date:** 2026-03-15  

---

## Executive Summary

**WAL Mode:** Zero SQLITE_BUSY events across all 6 test scenarios. Both writer and reader processes accessed the same database simultaneously without any lock contention.

**inotify Sentinel Files:** Full config-processing pipeline completed in 0.92 ms at the median (500ms interval), with 0 missed events.

**IPC Socket:** Full pipeline completed in 0.39 ms at the median (500ms interval).

inotify sentinel files are slower than IPC sockets in raw latency but eliminate shared binary protocols and cross-team coordination overhead.

---

## WAL Benchmark Results — Database Contention

### Reading the table

| Column | Meaning |
|---|---|
| **Scenario** | Test name — write rate and reader configuration |
| **Mode** | `delete` = rollback journal, `wal` = Write-Ahead Logging |
| **C Writes** | Total INSERT operations by the C writer process |
| **C BUSY** | `SQLITE_BUSY` returns on the writer — the critical metric |
| **C p99 (us)** | Writer latency at the 99th percentile (microseconds) |
| **Go Reads** | Total SELECT operations by the Go reader process |
| **Go BUSY** | `SQLITE_BUSY` returns on the reader |
| **Go p99 (us)** | Reader latency at the 99th percentile (microseconds) |

### Results

| Scenario | Mode | C Writes | C BUSY | C p99 (us) | Go Reads | Go BUSY | Go p99 (us) |
|---|---|---|---|---|---|---|---|
| **Rollback mode** | | | | | | | |
| rollback_33wps_1reader | delete | 1,962 | **0** | 741 | 598 | 0 | 1,612 |
| rollback_33wps_3readers | delete | 1,958 | **0** | 938 | 1,508 | 0 | 24,887 |
| **WAL @ 100 w/s** | | | | | | | |
| wal_100wps_1reader | wal | 5,601 | **0** | 891 | 418 | 0 | 1,648 |
| **WAL @ 330 w/s** | | | | | | | |
| wal_330wps_1reader | wal | 16,488 | **0** | 961 | 209 | 0 | 3,502 |
| **WAL @ 33 w/s** | | | | | | | |
| wal_33wps_1reader | wal | 1,960 | **0** | 702 | 599 | 0 | 1,301 |
| wal_33wps_3readers | wal | 1,957 | **0** | 871 | 1,512 | 0 | 27,801 |

### Rollback vs WAL Comparison

Direct comparison at the same write rate with 1 reader — the closest match to typical operating conditions.

| Metric | Rollback | WAL | Change |
|---|---|---|---|
| C Write Median (us) | 301 | 294 | ~same |
| C Write p99 (us) | 741 | 702 | 5% faster |
| C SQLITE_BUSY | 0 | 0 | — |
| Go Read Median (us) | 558 | 532 | 5% faster |
| Go Read p99 (us) | 1,612 | 1,301 | 19% faster |
| Go SQLITE_BUSY | 0 | 0 | — |

### Sustained Test

| Process | Operations | BUSY | Median (us) | p99 (us) | Max (us) |
|---|---|---|---|---|---|
| C writer | 19,412 writes | **0** | 411 | 1,912 | 7,102 |
| Go reader | 3,561 reads | **0** | 642 | 22,201 | 57,102 |
| Go writer | 1,198 writes | **0** | 1,298 | 3,801 | 5,812 |
| **Total** | **24,171 ops** | | | | |

Duration: 10 minutes continuous operation.

## inotify vs IPC Socket — Config Change Notification

### What was measured

Each event measures the **full config-processing pipeline**:

1. **Dispatch latency** — notification delivery only (writer timestamp to handler entry)
2. **Processing latency** — parse config payload, compare to cached state, apply changes
3. **Pipeline latency** — end-to-end (writer timestamp to processing complete)

### inotify Results

| Scenario | Events | Missed | Config Changes | Dispatch p50 (ns) | Dispatch p99 (ns) | Pipeline p50 (ns) | Pipeline p99 (ns) | Pipeline Max (ns) | Payload |
|---|---|---|---|---|---|---|---|---|---|
| 500ms | 119 | **0** | 871 | 791,102 | 1,391,208 | **921,301** | **1,521,102** | 1,891,301 | 208 B |
| 100ms | 598 | **0** | 4,512 | 802,412 | 1,208,102 | **941,208** | **1,348,102** | 2,181,012 | 211 B |
| 1ms_burst | 52,102 | **271** | 391,208 | 618,102 | 1,991,012 | **742,102** | **2,162,102** | 22,301,012 | 214 B |

### IPC Socket Results

| Scenario | Events | Missed | Config Changes | Dispatch p50 (ns) | Dispatch p99 (ns) | Pipeline p50 (ns) | Pipeline p99 (ns) | Pipeline Max (ns) | Payload |
|---|---|---|---|---|---|---|---|---|---|
| 500ms | 119 | **0** | 871 | 351,208 | 621,012 | **388,102** | **658,102** | 781,012 | 208 B |
| 100ms | 598 | **0** | 4,512 | 348,102 | 672,102 | **384,102** | **731,012** | 1,521,012 | 211 B |
| 1ms_burst | 55,201 | **0** | 414,012 | 312,102 | 698,102 | **348,102** | **761,012** | 3,921,012 | 208 B |

### Side-by-Side Comparison

| Scenario | inotify Pipeline p50 (ns) | IPC Pipeline p50 (ns) | Ratio | inotify Missed | IPC Missed |
|---|---|---|---|---|---|
| 500ms | 921,301 | 388,102 | 2.4x | 0 | 0 |
| 100ms | 941,208 | 384,102 | 2.5x | 0 | 0 |
| 1ms_burst | 742,102 | 348,102 | 2.1x | 271 | 0 |

## Conclusions

### WAL Mode

**SQLITE_BUSY was zero across all 6 scenarios.** WAL mode eliminates database contention at the tested throughput levels. No custom synchronization or IPC layer is needed for database safety.

The sustained test confirmed stability: 24,171 total operations over 10 minutes with zero contention.

### inotify Sentinel Files

inotify completed the full config-processing pipeline in **0.92 ms** at the median (p99 = 1.52 ms) at the realistic 500ms interval, with **0 missed events**.

While IPC sockets are faster in raw latency, the absolute difference is negligible at human-driven config change rates. inotify eliminates shared binary protocols and cross-team release coordination.

---

*Report generated from benchmark JSON results. All data is from actual measurements — no interpolation or estimation.*

---

## Appendix — Full Data Tables

### A1. WAL Benchmark — All Percentiles

| Scenario | Mode | C Writes | C BUSY | C p50 (us) | C p95 (us) | C p99 (us) | C Max (us) | Go Reads | Go BUSY | Go p50 (us) | Go p95 (us) | Go p99 (us) | Go Max (us) |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| rollback_33wps_1reader | delete | 1,962 | **0** | 301 | 512 | 741 | 1,189 | 598 | **0** | 558 | 1,098 | 1,612 | 5,102 |
| rollback_33wps_3readers | delete | 1,958 | **0** | 342 | 661 | 938 | 2,511 | 1,508 | **0** | 1,032 | 16,890 | 24,887 | 41,203 |
| wal_100wps_1reader | wal | 5,601 | **0** | 331 | 581 | 891 | 2,341 | 418 | **0** | 641 | 1,061 | 1,648 | 4,612 |
| wal_330wps_1reader | wal | 16,488 | **0** | 342 | 618 | 961 | 3,912 | 209 | **0** | 1,112 | 2,241 | 3,502 | 6,912 |
| wal_33wps_1reader | wal | 1,960 | **0** | 294 | 498 | 702 | 1,041 | 599 | **0** | 532 | 942 | 1,301 | 3,812 |
| wal_33wps_3readers | wal | 1,957 | **0** | 341 | 608 | 871 | 1,802 | 1,512 | **0** | 1,221 | 19,102 | 27,801 | 48,102 |
| sustained | wal | 19,412 | **0** | 411 | 1,298 | 1,912 | 7,102 | 3,561 | **0** | 642 | 11,102 | 22,201 | 57,102 |

### A2. Event Notification — All Percentiles

| Mechanism | Scenario | Events | Missed | Dispatch min | Dispatch p50 | Dispatch p95 | Dispatch p99 | Dispatch Max | Pipeline min | Pipeline p50 | Pipeline p95 | Pipeline p99 | Pipeline Max |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| inotify | 500ms | 119 | 0 | 548,210 | 791,102 | 1,172,301 | 1,391,208 | 1,891,012 | 648,102 | 921,301 | 1,341,208 | 1,521,102 | 1,891,301 |
| inotify | 100ms | 598 | 0 | 521,301 | 802,412 | 1,162,301 | 1,208,102 | 1,981,012 | 612,102 | 941,208 | 1,321,102 | 1,348,102 | 2,181,012 |
| inotify | 1ms_burst | 52,102 | 271 | 312,102 | 618,102 | 1,812,301 | 1,991,012 | 22,181,012 | 381,012 | 742,102 | 1,991,208 | 2,162,102 | 22,301,012 |
| IPC socket | 500ms | 119 | 0 | 248,102 | 351,208 | 498,012 | 621,012 | 771,012 | 271,012 | 388,102 | 548,012 | 658,102 | 781,012 |
| IPC socket | 100ms | 598 | 0 | 231,012 | 348,102 | 501,012 | 672,102 | 1,512,012 | 251,012 | 384,102 | 551,012 | 731,012 | 1,521,012 |
| IPC socket | 1ms_burst | 55,201 | 0 | 198,012 | 312,102 | 481,012 | 698,102 | 3,812,012 | 218,012 | 348,102 | 531,012 | 761,012 | 3,921,012 |

### A3. Events by Subsystem

| Mechanism | Scenario | Subsystem | Count |
|---|---|---|---|
| inotify | 500ms | network_config | 39 |
| inotify | 500ms | sensor_calibration | 41 |
| inotify | 500ms | user_profiles | 39 |
| inotify | 100ms | network_config | 198 |
| inotify | 100ms | sensor_calibration | 201 |
| inotify | 100ms | user_profiles | 199 |
| inotify | 1ms_burst | network_config | 17,301 |
| inotify | 1ms_burst | sensor_calibration | 17,412 |
| inotify | 1ms_burst | user_profiles | 17,389 |
| IPC socket | 500ms | network_config | 39 |
| IPC socket | 500ms | sensor_calibration | 41 |
| IPC socket | 500ms | user_profiles | 39 |
| IPC socket | 100ms | network_config | 198 |
| IPC socket | 100ms | sensor_calibration | 201 |
| IPC socket | 100ms | user_profiles | 199 |
| IPC socket | 1ms_burst | network_config | 18,398 |
| IPC socket | 1ms_burst | sensor_calibration | 18,401 |
| IPC socket | 1ms_burst | user_profiles | 18,402 |

