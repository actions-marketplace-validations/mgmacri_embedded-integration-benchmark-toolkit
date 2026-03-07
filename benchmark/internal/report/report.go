// Package report generates verdict-first benchmark reports from JSON results.
//
// The report leads with a clear PASS/FAIL verdict table on page one, evaluated
// against configurable thresholds from bench.yaml. Every number links back to
// its source JSON file.
//
// Structure:
//  1. Verdict Table — PASS/FAIL per criterion (top of page 1)
//  2. Key Findings — 3-5 sentences summarizing what matters
//  3. Detailed Results — tables with full percentile data
//  4. Clean vs Stressed Comparison — if stress data exists
//  5. Architecture Guidance — coupling analysis derived from measured data
//  6. Appendix — raw data reference
//
// This replaces the template prose in generate_report.py with computed
// verdicts derived from actual measurements.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/config"
)

// Verdict represents a pass/fail evaluation of a single criterion.
type Verdict struct {
	Criterion string // e.g., "WAL SQLITE_BUSY rate"
	Threshold string // e.g., "≤ 1.0%"
	Measured  string // e.g., "0.0%"
	Pass      bool   // true = within threshold
	Source    string // JSON file path for traceability
}

// WALScenarioData holds parsed data for one WAL scenario.
type WALScenarioData struct {
	Name     string
	CWriter  map[string]interface{}
	GoReader map[string]interface{}
	GoWriter map[string]interface{}
}

// EventScenarioData holds parsed data for one event scenario.
type EventScenarioData struct {
	Scenario string
	Receiver map[string]interface{}
	Sender   map[string]interface{}
	RecvRole string // "watcher", "server", "reader"
}

// ReportData holds all loaded benchmark data.
type ReportData struct {
	WALClean     []WALScenarioData
	WALStress    []WALScenarioData
	WALSustained *WALScenarioData

	InotifyClean  []EventScenarioData
	InotifyStress []EventScenarioData
	IPCClean      []EventScenarioData
	IPCStress     []EventScenarioData
	SHMClean      []EventScenarioData
	SHMStress     []EventScenarioData

	SystemInfo string
}

// Generate produces a verdict-first markdown report.
func Generate(w io.Writer, data *ReportData, cfg *config.BenchConfig) error {
	var verdicts []Verdict

	// Header
	fmt.Fprintf(w, "# Integration Benchmark Report\n\n")
	fmt.Fprintf(w, "**Generated:** %s  \n", time.Now().Format("January 02, 2006 15:04 MST"))
	if data.SystemInfo != "" {
		fmt.Fprintf(w, "**Target:** %s  \n", firstLine(data.SystemInfo))
	}
	fmt.Fprintln(w)

	// === VERDICT TABLE (page 1) ===
	verdicts = append(verdicts, evaluateWALVerdicts(data, cfg)...)
	verdicts = append(verdicts, evaluateEventVerdicts(data, cfg)...)
	verdicts = append(verdicts, evaluateSustainedVerdicts(data, cfg)...)

	writeVerdictTable(w, verdicts)

	// === KEY FINDINGS ===
	writeKeyFindings(w, verdicts, data)

	// === DETAILED RESULTS ===
	if len(data.WALClean) > 0 {
		writeWALResults(w, "Clean Conditions", data.WALClean)
	}
	if len(data.WALStress) > 0 {
		writeWALResults(w, "Under System Stress", data.WALStress)
	}
	if data.WALSustained != nil {
		writeSustainedResults(w, data.WALSustained)
	}

	// Event results
	writeEventSection(w, "inotify Sentinel", data.InotifyClean, data.InotifyStress)
	writeEventSection(w, "Unix Domain Socket (IPC)", data.IPCClean, data.IPCStress)
	writeEventSection(w, "Shared Memory (mmap + FIFO)", data.SHMClean, data.SHMStress)

	// === CLEAN vs STRESSED ===
	if len(data.WALStress) > 0 || len(data.InotifyStress) > 0 || len(data.IPCStress) > 0 || len(data.SHMStress) > 0 {
		writeCleanVsStressed(w, data)
	}

	// === COMPLEXITY SIGNAL SCORECARD ===
	writeComplexityScorecard(w, data)

	// === ARCHITECTURE GUIDANCE ===
	writeArchitectureGuidance(w, data)

	// === THRESHOLDS REFERENCE ===
	writeThresholdsReference(w, cfg)

	return nil
}

// LoadResults reads JSON results from a directory structure.
func LoadResults(resultsDir string) (*ReportData, error) {
	data := &ReportData{}

	// System info
	sysPath := filepath.Join(resultsDir, "system_info.txt")
	if content, err := os.ReadFile(sysPath); err == nil {
		data.SystemInfo = strings.TrimSpace(string(content))
	}

	// WAL results
	data.WALClean = loadWALScenarios(filepath.Join(resultsDir, "wal_clean"))
	data.WALStress = loadWALScenarios(filepath.Join(resultsDir, "wal_stress"))
	sustained := loadWALSingle(filepath.Join(resultsDir, "wal_sustained"))
	if sustained != nil {
		data.WALSustained = sustained
	}

	// Event results
	data.InotifyClean = loadEventScenarios(filepath.Join(resultsDir, "inotify_clean"))
	data.InotifyStress = loadEventScenarios(filepath.Join(resultsDir, "inotify_stress"))
	data.IPCClean = loadEventScenarios(filepath.Join(resultsDir, "ipc_clean"))
	data.IPCStress = loadEventScenarios(filepath.Join(resultsDir, "ipc_stress"))
	data.SHMClean = loadEventScenarios(filepath.Join(resultsDir, "shm_clean"))
	data.SHMStress = loadEventScenarios(filepath.Join(resultsDir, "shm_stress"))

	return data, nil
}

// ---------- Verdict evaluation ----------

func evaluateWALVerdicts(data *ReportData, cfg *config.BenchConfig) []Verdict {
	var verdicts []Verdict
	if len(data.WALClean) == 0 {
		return verdicts
	}

	th := cfg.Thresholds.WAL

	for _, scen := range data.WALClean {
		if scen.CWriter == nil {
			continue
		}

		// SQLITE_BUSY percentage
		totalWrites := jsonInt(scen.CWriter, "total_writes")
		busyCount := jsonInt(scen.CWriter, "sqlite_busy_count")
		var busyPct float64
		if totalWrites > 0 {
			busyPct = float64(busyCount) / float64(totalWrites) * 100.0
		}

		if th.MaxBusyPct > 0 {
			verdicts = append(verdicts, Verdict{
				Criterion: fmt.Sprintf("WAL %s — SQLITE_BUSY", scen.Name),
				Threshold: fmt.Sprintf("≤ %.1f%%", th.MaxBusyPct),
				Measured:  fmt.Sprintf("%.2f%%", busyPct),
				Pass:      busyPct <= th.MaxBusyPct,
				Source:    fmt.Sprintf("wal_clean/%s/c_writer.json", scen.Name),
			})
		}

		// P99 write latency
		if th.MaxP99WriteLatencyUs > 0 {
			p99 := jsonLatency(scen.CWriter, "write_latency_us", "p99")
			verdicts = append(verdicts, Verdict{
				Criterion: fmt.Sprintf("WAL %s — p99 write", scen.Name),
				Threshold: fmt.Sprintf("≤ %s µs", fmtNum(int64(th.MaxP99WriteLatencyUs))),
				Measured:  fmt.Sprintf("%s µs", fmtNum(p99)),
				Pass:      p99 <= int64(th.MaxP99WriteLatencyUs),
				Source:    fmt.Sprintf("wal_clean/%s/c_writer.json", scen.Name),
			})
		}

		// P99 read latency
		if th.MaxP99ReadLatencyUs > 0 && scen.GoReader != nil {
			p99 := jsonLatency(scen.GoReader, "read_latency_us", "p99")
			verdicts = append(verdicts, Verdict{
				Criterion: fmt.Sprintf("WAL %s — p99 read", scen.Name),
				Threshold: fmt.Sprintf("≤ %s µs", fmtNum(int64(th.MaxP99ReadLatencyUs))),
				Measured:  fmt.Sprintf("%s µs", fmtNum(p99)),
				Pass:      p99 <= int64(th.MaxP99ReadLatencyUs),
				Source:    fmt.Sprintf("wal_clean/%s/go_reader.json", scen.Name),
			})
		}
	}

	return verdicts
}

func evaluateEventVerdicts(data *ReportData, cfg *config.BenchConfig) []Verdict {
	var verdicts []Verdict
	th := cfg.Thresholds.Events

	type namedEvents struct {
		name string
		data []EventScenarioData
	}

	for _, ne := range []namedEvents{
		{"inotify", data.InotifyClean},
		{"IPC", data.IPCClean},
		{"SHM", data.SHMClean},
	} {
		for _, scen := range ne.data {
			if scen.Receiver == nil {
				continue
			}

			// P99 dispatch latency — event JSON uses _ns suffix, threshold is in µs
			if th.MaxP99DispatchLatencyUs > 0 {
				p99ns := jsonLatency(scen.Receiver, "dispatch_latency_ns", "p99")
				p99us := p99ns / 1000 // ns → µs
				verdicts = append(verdicts, Verdict{
					Criterion: fmt.Sprintf("%s %s — p99 dispatch", ne.name, scen.Scenario),
					Threshold: fmt.Sprintf("≤ %s µs", fmtNum(int64(th.MaxP99DispatchLatencyUs))),
					Measured:  fmt.Sprintf("%s µs", fmtNum(p99us)),
					Pass:      p99us <= int64(th.MaxP99DispatchLatencyUs),
					Source:    fmt.Sprintf("%s_clean/%s", strings.ToLower(ne.name), scen.Scenario),
				})
			}

			// Missed events (for inotify)
			if ne.name == "inotify" && th.MaxMissedEventsPct >= 0 {
				totalEvents := jsonInt(scen.Receiver, "total_events")
				missed := jsonInt(scen.Receiver, "missed_events")
				var missedPct float64
				if totalEvents > 0 {
					missedPct = float64(missed) / float64(totalEvents+missed) * 100.0
				}

				verdicts = append(verdicts, Verdict{
					Criterion: fmt.Sprintf("inotify %s — missed events", scen.Scenario),
					Threshold: fmt.Sprintf("≤ %.1f%%", th.MaxMissedEventsPct),
					Measured:  fmt.Sprintf("%.2f%%", missedPct),
					Pass:      missedPct <= th.MaxMissedEventsPct,
					Source:    fmt.Sprintf("inotify_clean/watcher_%s.json", scen.Scenario),
				})
			}
		}
	}

	return verdicts
}

func evaluateSustainedVerdicts(data *ReportData, cfg *config.BenchConfig) []Verdict {
	var verdicts []Verdict
	if data.WALSustained == nil || data.WALSustained.CWriter == nil {
		return verdicts
	}

	th := cfg.Thresholds.Sustained
	cw := data.WALSustained.CWriter

	if th.MaxBusyPct > 0 {
		totalWrites := jsonInt(cw, "total_writes")
		busyCount := jsonInt(cw, "sqlite_busy_count")
		var busyPct float64
		if totalWrites > 0 {
			busyPct = float64(busyCount) / float64(totalWrites) * 100.0
		}

		verdicts = append(verdicts, Verdict{
			Criterion: "Sustained — SQLITE_BUSY",
			Threshold: fmt.Sprintf("≤ %.1f%%", th.MaxBusyPct),
			Measured:  fmt.Sprintf("%.2f%%", busyPct),
			Pass:      busyPct <= th.MaxBusyPct,
			Source:    "wal_sustained/c_writer.json",
		})
	}

	if th.MaxP99WriteLatencyUs > 0 {
		p99 := jsonLatency(cw, "write_latency_us", "p99")
		verdicts = append(verdicts, Verdict{
			Criterion: "Sustained — p99 write",
			Threshold: fmt.Sprintf("≤ %s µs", fmtNum(int64(th.MaxP99WriteLatencyUs))),
			Measured:  fmt.Sprintf("%s µs", fmtNum(p99)),
			Pass:      p99 <= int64(th.MaxP99WriteLatencyUs),
			Source:    "wal_sustained/c_writer.json",
		})
	}

	return verdicts
}

// ---------- Report sections ----------

func writeVerdictTable(w io.Writer, verdicts []Verdict) {
	if len(verdicts) == 0 {
		return
	}

	passCount := 0
	for _, v := range verdicts {
		if v.Pass {
			passCount++
		}
	}
	failCount := len(verdicts) - passCount

	fmt.Fprintf(w, "## Verdict: ")
	if failCount == 0 {
		fmt.Fprintf(w, "ALL PASS (%d/%d)\n\n", passCount, len(verdicts))
	} else {
		fmt.Fprintf(w, "%d FAIL, %d PASS (of %d criteria)\n\n", failCount, passCount, len(verdicts))
	}

	fmt.Fprintf(w, "| Status | Criterion | Threshold | Measured | Source |\n")
	fmt.Fprintf(w, "|--------|-----------|-----------|----------|--------|\n")

	// Failures first
	for _, v := range verdicts {
		if !v.Pass {
			fmt.Fprintf(w, "| FAIL | %s | %s | **%s** | %s |\n", v.Criterion, v.Threshold, v.Measured, v.Source)
		}
	}
	for _, v := range verdicts {
		if v.Pass {
			fmt.Fprintf(w, "| PASS | %s | %s | %s | %s |\n", v.Criterion, v.Threshold, v.Measured, v.Source)
		}
	}

	fmt.Fprintln(w)
}

func writeKeyFindings(w io.Writer, verdicts []Verdict, data *ReportData) {
	fmt.Fprintf(w, "## Key Findings\n\n")

	failCount := 0
	for _, v := range verdicts {
		if !v.Pass {
			failCount++
		}
	}

	if failCount == 0 {
		fmt.Fprintf(w, "All %d measured criteria are within configured thresholds.\n\n", len(verdicts))
	} else {
		fmt.Fprintf(w, "%d of %d criteria exceeded their thresholds:\n\n", failCount, len(verdicts))
		for _, v := range verdicts {
			if !v.Pass {
				fmt.Fprintf(w, "- **%s**: measured %s (threshold: %s) — source: %s\n",
					v.Criterion, v.Measured, v.Threshold, v.Source)
			}
		}
		fmt.Fprintln(w)
	}

	// Transport comparison if we have all three
	if len(data.InotifyClean) > 0 && len(data.IPCClean) > 0 && len(data.SHMClean) > 0 {
		inoP99 := bestP99Dispatch(data.InotifyClean)
		ipcP99 := bestP99Dispatch(data.IPCClean)
		shmP99 := bestP99Dispatch(data.SHMClean)

		if inoP99 > 0 && ipcP99 > 0 && shmP99 > 0 {
			type ranked struct {
				name string
				p99  int64
			}
			ranks := []ranked{{"inotify", inoP99}, {"IPC", ipcP99}, {"SHM", shmP99}}
			sort.Slice(ranks, func(i, j int) bool { return ranks[i].p99 < ranks[j].p99 })

			fmt.Fprintf(w, "**Transport ranking by p99 dispatch latency (500ms scenario):** ")
			for i, r := range ranks {
				if i > 0 {
					fmt.Fprintf(w, " < ")
				}
				fmt.Fprintf(w, "%s (%s µs)", r.name, fmtNum(r.p99))
			}
			fmt.Fprintf(w, "\n\n")
		}
	}
}

func writeWALResults(w io.Writer, condition string, scenarios []WALScenarioData) {
	fmt.Fprintf(w, "## SQLite WAL Results — %s\n\n", condition)

	// Writer table
	fmt.Fprintf(w, "### C Writer Performance\n\n")
	fmt.Fprintf(w, "| Scenario | Journal | Writes | Busy | Errors | p50 µs | p95 µs | p99 µs | Max µs |\n")
	fmt.Fprintf(w, "|----------|---------|--------|------|--------|--------|--------|--------|--------|\n")

	for _, scen := range scenarios {
		if scen.CWriter == nil {
			continue
		}
		cw := scen.CWriter
		journal := jsonStr(cw, "journal_mode")
		writes := jsonInt(cw, "successful_writes")
		busy := jsonInt(cw, "sqlite_busy_count")
		errors := jsonInt(cw, "sqlite_error_count")

		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			scen.Name, journal,
			fmtNum(writes), fmtNum(busy), fmtNum(errors),
			fmtNum(jsonLatency(cw, "write_latency_us", "p50")),
			fmtNum(jsonLatency(cw, "write_latency_us", "p95")),
			fmtNum(jsonLatency(cw, "write_latency_us", "p99")),
			fmtNum(jsonLatency(cw, "write_latency_us", "max")))
	}

	fmt.Fprintln(w)

	// Reader table
	hasReader := false
	for _, scen := range scenarios {
		if scen.GoReader != nil {
			hasReader = true
			break
		}
	}
	if hasReader {
		fmt.Fprintf(w, "### Go Reader Performance\n\n")
		fmt.Fprintf(w, "| Scenario | Readers | Reads | Busy | Rows/Read | p50 µs | p95 µs | p99 µs | Max µs |\n")
		fmt.Fprintf(w, "|----------|---------|-------|------|-----------|--------|--------|--------|--------|\n")

		for _, scen := range scenarios {
			if scen.GoReader == nil {
				continue
			}
			gr := scen.GoReader
			readers := jsonInt(gr, "num_readers")
			reads := jsonInt(gr, "successful_reads")
			busy := jsonInt(gr, "sqlite_busy_count")
			rowsAvg := jsonInt(gr, "rows_returned_avg")
			if readers == 0 {
				readers = 1 // backwards compat: old files may not have num_readers
			}

			fmt.Fprintf(w, "| %s | %d | %s | %s | %s | %s | %s | %s | %s |\n",
				scen.Name, readers,
				fmtNum(reads), fmtNum(busy), fmtNum(rowsAvg),
				fmtNum(jsonLatency(gr, "read_latency_us", "p50")),
				fmtNum(jsonLatency(gr, "read_latency_us", "p95")),
				fmtNum(jsonLatency(gr, "read_latency_us", "p99")),
				fmtNum(jsonLatency(gr, "read_latency_us", "max")))
		}
		fmt.Fprintln(w)
	}
}

func writeSustainedResults(w io.Writer, scen *WALScenarioData) {
	fmt.Fprintf(w, "## Sustained WAL Test\n\n")

	if scen.CWriter != nil {
		cw := scen.CWriter
		fmt.Fprintf(w, "**C Writer:** %s writes, %s busy, p99=%s µs  \n",
			fmtNum(jsonInt(cw, "successful_writes")),
			fmtNum(jsonInt(cw, "sqlite_busy_count")),
			fmtNum(jsonLatency(cw, "write_latency_us", "p99")))
	}
	if scen.GoReader != nil {
		gr := scen.GoReader
		fmt.Fprintf(w, "**Go Reader:** %s reads, %s busy, p99=%s µs  \n",
			fmtNum(jsonInt(gr, "successful_reads")),
			fmtNum(jsonInt(gr, "sqlite_busy_count")),
			fmtNum(jsonLatency(gr, "read_latency_us", "p99")))
	}
	if scen.GoWriter != nil {
		gw := scen.GoWriter
		fmt.Fprintf(w, "**Go Writer:** %s writes, %s busy, p99=%s µs  \n",
			fmtNum(jsonInt(gw, "successful_writes")),
			fmtNum(jsonInt(gw, "sqlite_busy_count")),
			fmtNum(jsonLatency(gw, "write_latency_us", "p99")))
	}
	fmt.Fprintln(w)
}

func writeEventSection(w io.Writer, title string, clean, stress []EventScenarioData) {
	if len(clean) == 0 && len(stress) == 0 {
		return
	}

	fmt.Fprintf(w, "## %s\n\n", title)

	if len(clean) > 0 {
		writeEventTable(w, "Clean", clean)
	}
	if len(stress) > 0 {
		writeEventTable(w, "Stressed", stress)
	}
}

func writeEventTable(w io.Writer, label string, events []EventScenarioData) {
	fmt.Fprintf(w, "### %s Conditions\n\n", label)
	fmt.Fprintf(w, "| Scenario | Events | Dispatch p50 µs | Dispatch p99 µs | Process p99 µs | Pipeline p99 µs |\n")
	fmt.Fprintf(w, "|----------|--------|-----------------|-----------------|----------------|-----------------|\n")

	for _, scen := range events {
		if scen.Receiver == nil {
			continue
		}
		r := scen.Receiver
		totalEvt := jsonInt(r, "total_events")

		// Event JSON uses _ns suffixes; convert to µs for display
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s |\n",
			scen.Scenario, fmtNum(totalEvt),
			fmtNum(jsonLatency(r, "dispatch_latency_ns", "p50")/1000),
			fmtNum(jsonLatency(r, "dispatch_latency_ns", "p99")/1000),
			fmtNum(jsonLatency(r, "processing_latency_ns", "p99")/1000),
			fmtNum(jsonPipelineLatency(r, "p99")/1000))
	}
	fmt.Fprintln(w)
}

func writeCleanVsStressed(w io.Writer, data *ReportData) {
	fmt.Fprintf(w, "## Clean vs Stressed Comparison\n\n")

	// WAL comparison
	if len(data.WALClean) > 0 && len(data.WALStress) > 0 {
		fmt.Fprintf(w, "### WAL Write Latency Impact\n\n")
		fmt.Fprintf(w, "| Scenario | Clean p99 µs | Stressed p99 µs | Delta |\n")
		fmt.Fprintf(w, "|----------|-------------|-----------------|-------|\n")

		stressMap := make(map[string]WALScenarioData)
		for _, s := range data.WALStress {
			stressMap[s.Name] = s
		}

		for _, clean := range data.WALClean {
			if stressed, ok := stressMap[clean.Name]; ok && clean.CWriter != nil && stressed.CWriter != nil {
				cleanP99 := jsonLatency(clean.CWriter, "write_latency_us", "p99")
				stressP99 := jsonLatency(stressed.CWriter, "write_latency_us", "p99")
				delta := ""
				if cleanP99 > 0 {
					pct := float64(stressP99-cleanP99) / float64(cleanP99) * 100.0
					delta = fmt.Sprintf("%+.1f%%", pct)
				}
				fmt.Fprintf(w, "| %s | %s | %s | %s |\n",
					clean.Name, fmtNum(cleanP99), fmtNum(stressP99), delta)
			}
		}
		fmt.Fprintln(w)
	}

	// Event comparison
	type eventPair struct {
		name   string
		clean  []EventScenarioData
		stress []EventScenarioData
	}
	for _, ep := range []eventPair{
		{"inotify", data.InotifyClean, data.InotifyStress},
		{"IPC", data.IPCClean, data.IPCStress},
		{"SHM", data.SHMClean, data.SHMStress},
	} {
		if len(ep.clean) > 0 && len(ep.stress) > 0 {
			fmt.Fprintf(w, "### %s Dispatch Latency Impact\n\n", ep.name)
			fmt.Fprintf(w, "| Scenario | Clean p99 µs | Stressed p99 µs | Delta |\n")
			fmt.Fprintf(w, "|----------|-------------|-----------------|-------|\n")

			stressMap := make(map[string]EventScenarioData)
			for _, s := range ep.stress {
				stressMap[s.Scenario] = s
			}

			for _, clean := range ep.clean {
				if stressed, ok := stressMap[clean.Scenario]; ok && clean.Receiver != nil && stressed.Receiver != nil {
					cleanP99 := jsonLatency(clean.Receiver, "dispatch_latency_ns", "p99") / 1000
					stressP99 := jsonLatency(stressed.Receiver, "dispatch_latency_ns", "p99") / 1000
					delta := ""
					if cleanP99 > 0 {
						pct := float64(stressP99-cleanP99) / float64(cleanP99) * 100.0
						delta = fmt.Sprintf("%+.1f%%", pct)
					}
					fmt.Fprintf(w, "| %s | %s | %s | %s |\n",
						clean.Scenario, fmtNum(cleanP99), fmtNum(stressP99), delta)
				}
			}
			fmt.Fprintln(w)
		}
	}
}

// ---------- Complexity Signal Scorecard ----------

// complexitySignal represents one measured indicator of engineering complexity.
type complexitySignal struct {
	Signal      string // e.g., "SQLITE_BUSY detected"
	Transport   string // e.g., "WAL", "inotify", "SHM"
	Measured    string // e.g., "0 events across 13 scenarios"
	Source      string // JSON file path(s)
	Implication string // e.g., "Need retry/backoff logic"
	Triggered   bool   // true if the signal indicates additional work
}

func writeComplexityScorecard(w io.Writer, data *ReportData) {
	signals := collectComplexitySignals(data)
	if len(signals) == 0 {
		return
	}

	fmt.Fprintf(w, "## Integration Complexity Scorecard\n\n")
	fmt.Fprintf(w, "Each row is a **measured** indicator of engineering work required "+
		"to productionize this integration pattern. Signals are derived from the raw "+
		"JSON results above — not estimated.\n\n")

	triggered := 0
	for _, s := range signals {
		if s.Triggered {
			triggered++
		}
	}

	fmt.Fprintf(w, "**%d of %d signals triggered.**\n\n", triggered, len(signals))
	fmt.Fprintf(w, "| Status | Signal | Transport | Measured | Engineering Implication | Source |\n")
	fmt.Fprintf(w, "|--------|--------|-----------|----------|------------------------|--------|\n")

	// Triggered first, then clear
	for _, s := range signals {
		if s.Triggered {
			fmt.Fprintf(w, "| **TRIGGERED** | %s | %s | %s | %s | %s |\n",
				s.Signal, s.Transport, s.Measured, s.Implication, s.Source)
		}
	}
	for _, s := range signals {
		if !s.Triggered {
			fmt.Fprintf(w, "| clear | %s | %s | %s | %s | %s |\n",
				s.Signal, s.Transport, s.Measured, s.Implication, s.Source)
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "*This scorecard surfaces failure modes that create engineering work. "+
		"Apply your team's cost model to estimate effort — the toolkit measures, it does not guess.*\n\n")
}

func collectComplexitySignals(data *ReportData) []complexitySignal {
	var signals []complexitySignal

	// 1. SQLITE_BUSY — need retry/backoff logic
	signals = append(signals, checkSQLiteBusy(data)...)

	// 2. Missed events (inotify) — need reconciliation path
	signals = append(signals, checkMissedEvents(data)...)

	// 3. Coalesced events (inotify) — need idempotent handlers
	signals = append(signals, checkCoalescedEvents(data)...)

	// 4. Sequence errors (SHM) — need error recovery
	signals = append(signals, checkSequenceErrors(data)...)

	// 5. Tail latency ratio (p99/p50 > 10) — need timeout tuning
	signals = append(signals, checkTailLatencyRatio(data)...)

	// 6. Stress amplification (stress p99 / clean p99 > 3) — need resource isolation
	signals = append(signals, checkStressAmplification(data)...)

	return signals
}

func checkSQLiteBusy(data *ReportData) []complexitySignal {
	if len(data.WALClean) == 0 {
		return nil
	}

	totalBusy := int64(0)
	scenCount := 0
	for _, scen := range data.WALClean {
		if scen.CWriter != nil {
			totalBusy += jsonInt(scen.CWriter, "sqlite_busy_count")
			scenCount++
		}
		if scen.GoReader != nil {
			totalBusy += jsonInt(scen.GoReader, "sqlite_busy_count")
		}
	}

	return []complexitySignal{{
		Signal:      "SQLITE_BUSY contention",
		Transport:   "WAL",
		Measured:    fmt.Sprintf("%s events across %d scenarios", fmtNum(totalBusy), scenCount),
		Source:      "wal_clean/*/c_writer.json, go_reader.json",
		Implication: "Retry/backoff logic, busy_timeout tuning, write queue management",
		Triggered:   totalBusy > 0,
	}}
}

func checkMissedEvents(data *ReportData) []complexitySignal {
	if len(data.InotifyClean) == 0 {
		return nil
	}

	totalMissed := int64(0)
	totalEvents := int64(0)
	worstScenario := ""
	worstMissed := int64(0)
	for _, scen := range data.InotifyClean {
		if scen.Receiver == nil {
			continue
		}
		missed := jsonInt(scen.Receiver, "missed_events")
		events := jsonInt(scen.Receiver, "total_events")
		totalMissed += missed
		totalEvents += events
		if missed > worstMissed {
			worstMissed = missed
			worstScenario = scen.Scenario
		}
	}

	measured := fmt.Sprintf("%s missed of %s total", fmtNum(totalMissed), fmtNum(totalEvents))
	if worstMissed > 0 {
		measured += fmt.Sprintf(" (worst: %s in %s)", fmtNum(worstMissed), worstScenario)
	}

	return []complexitySignal{{
		Signal:      "inotify missed events",
		Transport:   "inotify",
		Measured:    measured,
		Source:      "inotify_clean/watcher_*.json",
		Implication: "Full-state reconciliation path, periodic re-read fallback",
		Triggered:   totalMissed > 0,
	}}
}

func checkCoalescedEvents(data *ReportData) []complexitySignal {
	if len(data.InotifyClean) == 0 {
		return nil
	}

	totalCoalesced := int64(0)
	totalEvents := int64(0)
	for _, scen := range data.InotifyClean {
		if scen.Receiver == nil {
			continue
		}
		totalCoalesced += jsonInt(scen.Receiver, "coalesced_events")
		totalEvents += jsonInt(scen.Receiver, "total_events")
	}

	return []complexitySignal{{
		Signal:      "inotify event coalescing",
		Transport:   "inotify",
		Measured:    fmt.Sprintf("%s coalesced of %s total events", fmtNum(totalCoalesced), fmtNum(totalEvents)),
		Source:      "inotify_clean/watcher_*.json",
		Implication: "Idempotent config handlers, full-file re-parse on every event",
		Triggered:   totalCoalesced > 0,
	}}
}

func checkSequenceErrors(data *ReportData) []complexitySignal {
	if len(data.SHMClean) == 0 {
		return nil
	}

	totalErrors := int64(0)
	totalEvents := int64(0)
	for _, scen := range data.SHMClean {
		if scen.Receiver == nil {
			continue
		}
		totalErrors += jsonInt(scen.Receiver, "sequence_errors")
		totalEvents += jsonInt(scen.Receiver, "total_events")
	}

	return []complexitySignal{{
		Signal:      "SHM sequence errors",
		Transport:   "SHM",
		Measured:    fmt.Sprintf("%s errors across %s events", fmtNum(totalErrors), fmtNum(totalEvents)),
		Source:      "shm_clean/reader_*.json",
		Implication: "Torn-read recovery, sequence validation, retry on mismatch",
		Triggered:   totalErrors > 0,
	}}
}

func checkTailLatencyRatio(data *ReportData) []complexitySignal {
	var signals []complexitySignal

	// WAL tail ratio
	if len(data.WALClean) > 0 {
		worstRatio := 0.0
		worstScenario := ""
		for _, scen := range data.WALClean {
			if scen.CWriter == nil {
				continue
			}
			p50 := jsonLatency(scen.CWriter, "write_latency_us", "p50")
			p99 := jsonLatency(scen.CWriter, "write_latency_us", "p99")
			if p50 > 0 {
				ratio := float64(p99) / float64(p50)
				if ratio > worstRatio {
					worstRatio = ratio
					worstScenario = scen.Name
				}
			}
		}
		signals = append(signals, complexitySignal{
			Signal:      "WAL write tail latency (p99/p50)",
			Transport:   "WAL",
			Measured:    fmt.Sprintf("%.1fx (worst: %s)", worstRatio, worstScenario),
			Source:      fmt.Sprintf("wal_clean/%s/c_writer.json", worstScenario),
			Implication: "Timeout tuning, adaptive retry intervals, p99-aware SLA design",
			Triggered:   worstRatio > 10.0,
		})
	}

	// Event transports tail ratio
	type eventTransport struct {
		name      string
		data      []EventScenarioData
		latKey    string
		sourceDir string
	}
	for _, et := range []eventTransport{
		{"inotify", data.InotifyClean, "dispatch_latency_ns", "inotify_clean"},
		{"IPC", data.IPCClean, "dispatch_latency_ns", "ipc_clean"},
		{"SHM", data.SHMClean, "dispatch_latency_ns", "shm_clean"},
	} {
		if len(et.data) == 0 {
			continue
		}
		worstRatio := 0.0
		worstScenario := ""
		for _, scen := range et.data {
			if scen.Receiver == nil {
				continue
			}
			p50 := jsonLatency(scen.Receiver, et.latKey, "p50")
			p99 := jsonLatency(scen.Receiver, et.latKey, "p99")
			if p50 > 0 {
				ratio := float64(p99) / float64(p50)
				if ratio > worstRatio {
					worstRatio = ratio
					worstScenario = scen.Scenario
				}
			}
		}
		if worstScenario != "" {
			signals = append(signals, complexitySignal{
				Signal:      fmt.Sprintf("%s dispatch tail latency (p99/p50)", et.name),
				Transport:   et.name,
				Measured:    fmt.Sprintf("%.1fx (worst: %s)", worstRatio, worstScenario),
				Source:      fmt.Sprintf("%s/%s", et.sourceDir, worstScenario),
				Implication: "Timeout tuning, adaptive retry intervals, p99-aware SLA design",
				Triggered:   worstRatio > 10.0,
			})
		}
	}

	return signals
}

func checkStressAmplification(data *ReportData) []complexitySignal {
	var signals []complexitySignal

	// WAL stress amplification
	if len(data.WALClean) > 0 && len(data.WALStress) > 0 {
		stressMap := make(map[string]WALScenarioData)
		for _, s := range data.WALStress {
			stressMap[s.Name] = s
		}
		worstRatio := 0.0
		worstScenario := ""
		for _, clean := range data.WALClean {
			if clean.CWriter == nil {
				continue
			}
			stressed, ok := stressMap[clean.Name]
			if !ok || stressed.CWriter == nil {
				continue
			}
			cleanP99 := jsonLatency(clean.CWriter, "write_latency_us", "p99")
			stressP99 := jsonLatency(stressed.CWriter, "write_latency_us", "p99")
			if cleanP99 > 0 {
				ratio := float64(stressP99) / float64(cleanP99)
				if ratio > worstRatio {
					worstRatio = ratio
					worstScenario = clean.Name
				}
			}
		}
		if worstScenario != "" {
			signals = append(signals, complexitySignal{
				Signal:      "WAL stress amplification (stressed/clean p99)",
				Transport:   "WAL",
				Measured:    fmt.Sprintf("%.1fx (worst: %s)", worstRatio, worstScenario),
				Source:      fmt.Sprintf("wal_stress/%s vs wal_clean/%s", worstScenario, worstScenario),
				Implication: "CPU/IO isolation (cgroups), priority scheduling, dedicated cores",
				Triggered:   worstRatio > 3.0,
			})
		}
	}

	// Event stress amplification
	type eventPair struct {
		name      string
		clean     []EventScenarioData
		stress    []EventScenarioData
		sourceDir string
	}
	for _, ep := range []eventPair{
		{"inotify", data.InotifyClean, data.InotifyStress, "inotify"},
		{"IPC", data.IPCClean, data.IPCStress, "ipc"},
		{"SHM", data.SHMClean, data.SHMStress, "shm"},
	} {
		if len(ep.clean) == 0 || len(ep.stress) == 0 {
			continue
		}
		stressMap := make(map[string]EventScenarioData)
		for _, s := range ep.stress {
			stressMap[s.Scenario] = s
		}
		worstRatio := 0.0
		worstScenario := ""
		for _, clean := range ep.clean {
			if clean.Receiver == nil {
				continue
			}
			stressed, ok := stressMap[clean.Scenario]
			if !ok || stressed.Receiver == nil {
				continue
			}
			cleanP99 := jsonLatency(clean.Receiver, "dispatch_latency_ns", "p99")
			stressP99 := jsonLatency(stressed.Receiver, "dispatch_latency_ns", "p99")
			if cleanP99 > 0 {
				ratio := float64(stressP99) / float64(cleanP99)
				if ratio > worstRatio {
					worstRatio = ratio
					worstScenario = clean.Scenario
				}
			}
		}
		if worstScenario != "" {
			signals = append(signals, complexitySignal{
				Signal:      fmt.Sprintf("%s stress amplification (stressed/clean p99)", ep.name),
				Transport:   ep.name,
				Measured:    fmt.Sprintf("%.1fx (worst: %s)", worstRatio, worstScenario),
				Source:      fmt.Sprintf("%s_stress/%s vs %s_clean/%s", ep.sourceDir, worstScenario, ep.sourceDir, worstScenario),
				Implication: "CPU/IO isolation (cgroups), priority scheduling, dedicated cores",
				Triggered:   worstRatio > 3.0,
			})
		}
	}

	return signals
}

func writeArchitectureGuidance(w io.Writer, data *ReportData) {
	if len(data.InotifyClean) == 0 && len(data.IPCClean) == 0 && len(data.SHMClean) == 0 {
		return
	}

	fmt.Fprintf(w, "## Architecture Guidance\n\n")
	fmt.Fprintf(w, "The following analysis is derived from the measured data above. "+
		"It evaluates each transport mechanism on three axes: latency, coupling, and reliability.\n\n")

	fmt.Fprintf(w, "| Transport | p99 Dispatch µs | Binary Coupling | Build Coupling | Release Coupling |\n")
	fmt.Fprintf(w, "|-----------|-----------------|-----------------|----------------|------------------|\n")

	type transport struct {
		name            string
		data            []EventScenarioData
		binaryCoupling  string
		buildCoupling   string
		releaseCoupling string
	}

	transports := []transport{
		{"inotify", data.InotifyClean, "None (filesystem)", "None", "Independent"},
		{"IPC", data.IPCClean, "Protocol (socket)", "Shared format", "Coordinated"},
		{"SHM", data.SHMClean, "Struct layout (mmap)", "Shared header", "Locked"},
	}

	for _, t := range transports {
		p99 := "-"
		if len(t.data) > 0 {
			v := bestP99Dispatch(t.data)
			if v > 0 {
				p99 = fmtNum(v)
			}
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s |\n",
			t.name, p99, t.binaryCoupling, t.buildCoupling, t.releaseCoupling)
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "**Interpretation:** Lower coupling means each team can build, test, "+
		"and deploy independently. The latency difference between transports is typically "+
		"sub-millisecond — negligible for configuration changes that happen at most every "+
		"100ms. Choose based on your coupling tolerance, not raw speed.\n\n")
}

func writeThresholdsReference(w io.Writer, cfg *config.BenchConfig) {
	fmt.Fprintf(w, "## Appendix: Configured Thresholds\n\n")
	fmt.Fprintf(w, "| Category | Criterion | Threshold |\n")
	fmt.Fprintf(w, "|----------|-----------|----------|\n")

	th := cfg.Thresholds
	if th.WAL.MaxBusyPct > 0 {
		fmt.Fprintf(w, "| WAL | SQLITE_BUSY rate | ≤ %.1f%% |\n", th.WAL.MaxBusyPct)
	}
	if th.WAL.MaxP99WriteLatencyUs > 0 {
		fmt.Fprintf(w, "| WAL | p99 write latency | ≤ %s µs |\n", fmtNum(int64(th.WAL.MaxP99WriteLatencyUs)))
	}
	if th.WAL.MaxP99ReadLatencyUs > 0 {
		fmt.Fprintf(w, "| WAL | p99 read latency | ≤ %s µs |\n", fmtNum(int64(th.WAL.MaxP99ReadLatencyUs)))
	}
	if th.Events.MaxP99DispatchLatencyUs > 0 {
		fmt.Fprintf(w, "| Events | p99 dispatch latency | ≤ %s µs |\n", fmtNum(int64(th.Events.MaxP99DispatchLatencyUs)))
	}
	if th.Events.MaxMissedEventsPct >= 0 {
		fmt.Fprintf(w, "| Events | Missed events | ≤ %.1f%% |\n", th.Events.MaxMissedEventsPct)
	}
	if th.Events.MaxCoalescedPct > 0 {
		fmt.Fprintf(w, "| Events | Coalesced events | ≤ %.1f%% |\n", th.Events.MaxCoalescedPct)
	}
	if th.Sustained.MaxBusyPct > 0 {
		fmt.Fprintf(w, "| Sustained | SQLITE_BUSY rate | ≤ %.1f%% |\n", th.Sustained.MaxBusyPct)
	}
	if th.Sustained.MaxP99WriteLatencyUs > 0 {
		fmt.Fprintf(w, "| Sustained | p99 write latency | ≤ %s µs |\n", fmtNum(int64(th.Sustained.MaxP99WriteLatencyUs)))
	}
	fmt.Fprintln(w)
}

// ---------- Data loading helpers ----------

func loadWALScenarios(dir string) []WALScenarioData {
	var scenarios []WALScenarioData
	entries, err := os.ReadDir(dir)
	if err != nil {
		return scenarios
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		scen := loadWALSingle(filepath.Join(dir, entry.Name()))
		if scen != nil {
			scen.Name = entry.Name()
			scenarios = append(scenarios, *scen)
		}
	}
	return scenarios
}

func loadWALSingle(dir string) *WALScenarioData {
	scen := &WALScenarioData{Name: filepath.Base(dir)}

	for _, name := range []string{"c_writer.json", "fw_writer.json"} {
		if data := loadJSON(filepath.Join(dir, name)); data != nil {
			scen.CWriter = data
			break
		}
	}
	for _, name := range []string{"go_reader.json", "sw_reader.json"} {
		if data := loadJSON(filepath.Join(dir, name)); data != nil {
			scen.GoReader = data
			break
		}
	}
	for _, name := range []string{"go_writer.json", "sw_writer.json"} {
		if data := loadJSON(filepath.Join(dir, name)); data != nil {
			scen.GoWriter = data
			break
		}
	}

	if scen.CWriter == nil && scen.GoReader == nil {
		return nil
	}
	return scen
}

func loadEventScenarios(dir string) []EventScenarioData {
	var results []EventScenarioData
	entries, err := os.ReadDir(dir)
	if err != nil {
		return results
	}

	// Discover scenarios from filenames
	scenarioSet := make(map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			parts := strings.SplitN(strings.TrimSuffix(entry.Name(), ".json"), "_", 2)
			if len(parts) == 2 {
				scenarioSet[parts[1]] = true
			}
		}
	}

	// Sort scenarios
	scenarios := make([]string, 0, len(scenarioSet))
	for s := range scenarioSet {
		scenarios = append(scenarios, s)
	}
	sort.Slice(scenarios, func(i, j int) bool {
		return scenarioSortKey(scenarios[i]) < scenarioSortKey(scenarios[j])
	})

	for _, scen := range scenarios {
		entry := EventScenarioData{Scenario: scen}

		for _, prefix := range []string{"watcher_", "server_", "reader_"} {
			if data := loadJSON(filepath.Join(dir, prefix+scen+".json")); data != nil {
				entry.Receiver = data
				entry.RecvRole = strings.TrimSuffix(prefix, "_")
				break
			}
		}
		for _, prefix := range []string{"writer_", "client_"} {
			if data := loadJSON(filepath.Join(dir, prefix+scen+".json")); data != nil {
				entry.Sender = data
				break
			}
		}

		if entry.Receiver != nil {
			results = append(results, entry)
		}
	}

	return results
}

// ---------- JSON helpers ----------

func loadJSON(path string) map[string]interface{} {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return result
}

func jsonInt(m map[string]interface{}, key string) int64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	default:
		return 0
	}
}

func jsonStr(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func jsonLatency(m map[string]interface{}, statsKey, pctKey string) int64 {
	if m == nil {
		return 0
	}
	stats, ok := m[statsKey]
	if !ok {
		return 0
	}
	statsMap, ok := stats.(map[string]interface{})
	if !ok {
		return 0
	}
	return jsonInt(statsMap, pctKey)
}

// jsonPipelineLatency returns a pipeline latency percentile, trying
// "total_pipeline_latency_ns" first (current C output) then falling back to
// "pipeline_latency_ns" (older JSON files / example data) for compatibility.
func jsonPipelineLatency(m map[string]interface{}, pctKey string) int64 {
	v := jsonLatency(m, "total_pipeline_latency_ns", pctKey)
	if v != 0 {
		return v
	}
	return jsonLatency(m, "pipeline_latency_ns", pctKey)
}

// ---------- Formatting helpers ----------

func fmtNum(n int64) string {
	if n == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		s = s[1:] // handle negative
	}
	// Insert commas
	var result strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result.WriteByte(',')
		}
		result.WriteRune(ch)
	}
	if n < 0 {
		return "-" + result.String()
	}
	return result.String()
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

func bestP99Dispatch(events []EventScenarioData) int64 {
	// Return p99 dispatch from the 500ms (realistic) scenario, in µs
	for _, e := range events {
		if strings.HasPrefix(e.Scenario, "500") && e.Receiver != nil {
			return jsonLatency(e.Receiver, "dispatch_latency_ns", "p99") / 1000
		}
	}
	// Fallback: first scenario
	if len(events) > 0 && events[0].Receiver != nil {
		return jsonLatency(events[0].Receiver, "dispatch_latency_ns", "p99") / 1000
	}
	return 0
}

func scenarioSortKey(s string) int {
	if strings.HasPrefix(s, "500") {
		return 0
	}
	if strings.HasPrefix(s, "100") && !strings.HasPrefix(s, "1000") {
		return 1
	}
	return 2
}

// ExitCode returns 0 if all verdicts pass, 1 if any fail.
// Suitable for CI pipeline integration.
func ExitCode(verdicts []Verdict) int {
	for _, v := range verdicts {
		if !v.Pass {
			return 1
		}
	}
	return 0
}
