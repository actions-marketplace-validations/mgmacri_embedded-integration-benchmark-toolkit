// Package main implements generate_report — walks results directory, parses JSON, generates markdown.
//
// CLI: ./generate_report <results_dir>
// Output: Markdown report to stdout
//
// Customize:
//   - Add additional result types for your benchmarks
//   - Modify conclusion logic to match your success criteria
//   - Adjust table columns for the metrics you care about
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Common latency stats structure
type LatencyStats struct {
	Min int64 `json:"min"`
	P50 int64 `json:"p50"`
	P95 int64 `json:"p95"`
	P99 int64 `json:"p99"`
	Max int64 `json:"max"`
}

// C Writer result
type CWriterResult struct {
	Role             string       `json:"role"`
	JournalMode      string       `json:"journal_mode"`
	BusyTimeoutMs    int          `json:"busy_timeout_ms"`
	IntervalMs       int          `json:"interval_ms"`
	DurationSec      int          `json:"duration_sec"`
	TotalWrites      int          `json:"total_writes"`
	SuccessfulWrites int          `json:"successful_writes"`
	SQLiteBusyCount  int          `json:"sqlite_busy_count"`
	SQLiteErrorCount int          `json:"sqlite_error_count"`
	WriteLatencyUs   LatencyStats `json:"write_latency_us"`
}

// Go Reader result
type GoReaderResult struct {
	Role              string       `json:"role"`
	JournalMode       string       `json:"journal_mode"`
	ConcurrentReaders int          `json:"concurrent_readers"`
	ReadIntervalMs    int          `json:"read_interval_ms"`
	DurationSec       int          `json:"duration_sec"`
	TotalReads        int64        `json:"total_reads"`
	SuccessfulReads   int64        `json:"successful_reads"`
	SQLiteBusyCount   int64        `json:"sqlite_busy_count"`
	SQLiteErrorCount  int64        `json:"sqlite_error_count"`
	RowsReturnedTotal int64        `json:"rows_returned_total"`
	RowsReturnedAvg   int64        `json:"rows_returned_avg"`
	ReadLatencyUs     LatencyStats `json:"read_latency_us"`
}

// Go Writer result
type GoWriterResult struct {
	Role             string       `json:"role"`
	JournalMode      string       `json:"journal_mode"`
	IntervalMs       int          `json:"interval_ms"`
	DurationSec      int          `json:"duration_sec"`
	TotalWrites      int          `json:"total_writes"`
	SuccessfulWrites int          `json:"successful_writes"`
	SQLiteBusyCount  int          `json:"sqlite_busy_count"`
	SQLiteErrorCount int          `json:"sqlite_error_count"`
	WriteLatencyUs   LatencyStats `json:"write_latency_us"`
}

// inotify Watcher result
type InotifyWatcherResult struct {
	Role                string         `json:"role"`
	TotalEvents         int            `json:"total_events"`
	MissedEvents        int            `json:"missed_events"`
	UnknownEvents       int            `json:"unknown_events"`
	OverflowEvents      int            `json:"overflow_events"`
	CoalescedEvents     int            `json:"coalesced_events"`
	ConfigChanges       int            `json:"config_changes_detected"`
	DispatchLatencyNs   LatencyStats   `json:"dispatch_latency_ns"`
	ProcessingLatencyNs LatencyStats   `json:"processing_latency_ns"`
	PipelineLatencyNs   LatencyStats   `json:"total_pipeline_latency_ns"`
	EventsBySubsystem   map[string]int `json:"events_by_subsystem"`
}

// IPC Socket Server result
type IPCServerResult struct {
	Role                string         `json:"role"`
	TotalEvents         int            `json:"total_events"`
	ConfigChanges       int            `json:"config_changes_detected"`
	DispatchLatencyNs   LatencyStats   `json:"dispatch_latency_ns"`
	ProcessingLatencyNs LatencyStats   `json:"processing_latency_ns"`
	PipelineLatencyNs   LatencyStats   `json:"total_pipeline_latency_ns"`
	EventsBySubsystem   map[string]int `json:"events_by_subsystem"`
}

// SHM Reader result
type SHMReaderResult struct {
	Role                string         `json:"role"`
	TotalEvents         int            `json:"total_events"`
	SequenceErrors      int            `json:"sequence_errors"`
	ConfigChanges       int            `json:"config_changes_detected"`
	DispatchLatencyNs   LatencyStats   `json:"dispatch_latency_ns"`
	ProcessingLatencyNs LatencyStats   `json:"processing_latency_ns"`
	PipelineLatencyNs   LatencyStats   `json:"total_pipeline_latency_ns"`
	EventsBySubsystem   map[string]int `json:"events_by_subsystem"`
}

// Scenario holds all results from a single WAL scenario directory
type WALScenario struct {
	Name     string
	CWriter  *CWriterResult
	GoReader *GoReaderResult
	GoWriter *GoWriterResult
}

func loadJSON(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func generateWALReport(resultsDir string) {
	var scenarios []WALScenario

	entries, err := os.ReadDir(resultsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading results dir: %v\n", err)
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		scenDir := filepath.Join(resultsDir, entry.Name())
		scen := WALScenario{Name: entry.Name()}

		// Try both naming conventions (c_writer / fw_writer)
		for _, name := range []string{"c_writer.json", "fw_writer.json"} {
			p := filepath.Join(scenDir, name)
			if _, err := os.Stat(p); err == nil {
				var cw CWriterResult
				if loadJSON(p, &cw) == nil {
					scen.CWriter = &cw
					break
				}
			}
		}

		for _, name := range []string{"go_reader.json", "sw_reader.json"} {
			p := filepath.Join(scenDir, name)
			if _, err := os.Stat(p); err == nil {
				var gr GoReaderResult
				if loadJSON(p, &gr) == nil {
					scen.GoReader = &gr
					break
				}
			}
		}

		for _, name := range []string{"go_writer.json", "sw_writer.json"} {
			p := filepath.Join(scenDir, name)
			if _, err := os.Stat(p); err == nil {
				var gw GoWriterResult
				if loadJSON(p, &gw) == nil {
					scen.GoWriter = &gw
					break
				}
			}
		}

		if scen.CWriter != nil {
			scenarios = append(scenarios, scen)
		}
	}

	sort.Slice(scenarios, func(i, j int) bool {
		return scenarios[i].Name < scenarios[j].Name
	})

	// Print system info if available
	sysInfo, err := os.ReadFile(filepath.Join(resultsDir, "system_info.txt"))
	if err == nil {
		fmt.Println("### System Info")
		fmt.Println("```")
		fmt.Print(string(sysInfo))
		fmt.Println("```")
		fmt.Println()
	}

	// Results table
	fmt.Println("### Results Summary")
	fmt.Println()
	fmt.Println("| Scenario | Mode | C Writes | C BUSY | C p99 (us) | Go Reads | Go BUSY | Go p99 (us) |")
	fmt.Println("|---|---|---|---|---|---|---|---|")

	for _, s := range scenarios {
		cw := s.CWriter
		cwBusy := cw.SQLiteBusyCount
		cwP99 := cw.WriteLatencyUs.P99

		goReads := "-"
		goBusy := "-"
		goP99 := "-"
		if s.GoReader != nil {
			goReads = fmt.Sprintf("%d", s.GoReader.TotalReads)
			goBusy = fmt.Sprintf("%d", s.GoReader.SQLiteBusyCount)
			goP99 = fmt.Sprintf("%d", s.GoReader.ReadLatencyUs.P99)
		}

		fmt.Printf("| %s | %s | %d | %d | %d | %s | %s | %s |\n",
			s.Name, cw.JournalMode, cw.TotalWrites, cwBusy, cwP99,
			goReads, goBusy, goP99)
	}
	fmt.Println()

	generateComparison(scenarios)
	generateConclusion(scenarios)
}

func generateComparison(scenarios []WALScenario) {
	var rollback, wal *WALScenario
	for i := range scenarios {
		if strings.Contains(scenarios[i].Name, "rollback_33wps_1reader") {
			rollback = &scenarios[i]
		}
		if scenarios[i].Name == "wal_33wps_1reader" {
			wal = &scenarios[i]
		}
	}

	if rollback == nil || wal == nil {
		return
	}

	fmt.Println("### Rollback vs WAL Comparison (33 w/s, 1 reader)")
	fmt.Println()
	fmt.Println("| Metric | Rollback Mode | WAL Mode |")
	fmt.Println("|---|---|---|")
	fmt.Printf("| C Write p50 (us) | %d | %d |\n", rollback.CWriter.WriteLatencyUs.P50, wal.CWriter.WriteLatencyUs.P50)
	fmt.Printf("| C Write p99 (us) | %d | %d |\n", rollback.CWriter.WriteLatencyUs.P99, wal.CWriter.WriteLatencyUs.P99)
	fmt.Printf("| C SQLITE_BUSY | %d | %d |\n", rollback.CWriter.SQLiteBusyCount, wal.CWriter.SQLiteBusyCount)

	if rollback.GoReader != nil && wal.GoReader != nil {
		fmt.Printf("| Go Read p50 (us) | %d | %d |\n", rollback.GoReader.ReadLatencyUs.P50, wal.GoReader.ReadLatencyUs.P50)
		fmt.Printf("| Go Read p99 (us) | %d | %d |\n", rollback.GoReader.ReadLatencyUs.P99, wal.GoReader.ReadLatencyUs.P99)
		fmt.Printf("| Go SQLITE_BUSY | %d | %d |\n", rollback.GoReader.SQLiteBusyCount, wal.GoReader.SQLiteBusyCount)
	}
	fmt.Println()
}

func generateConclusion(scenarios []WALScenario) {
	fmt.Println("### Conclusion")
	fmt.Println()

	walBusyC := 0
	walBusyGo := int64(0)
	walCount := 0
	for _, s := range scenarios {
		if strings.HasPrefix(s.Name, "wal_") {
			walBusyC += s.CWriter.SQLiteBusyCount
			if s.GoReader != nil {
				walBusyGo += s.GoReader.SQLiteBusyCount
			}
			walCount++
		}
	}

	if walBusyC == 0 && walBusyGo == 0 && walCount > 0 {
		fmt.Println("**SQLITE_BUSY count is ZERO across all WAL mode scenarios.** WAL mode eliminates database contention at the tested throughput levels. No IPC layer is needed for database safety.")
	} else {
		fmt.Printf("WAL mode scenarios: C BUSY total=%d, Go BUSY total=%d across %d scenarios.\n",
			walBusyC, walBusyGo, walCount)
		if walBusyC > 0 || walBusyGo > 0 {
			fmt.Println("SQLITE_BUSY events detected — consider increasing busy_timeout or reducing write frequency.")
		}
	}
	fmt.Println()
}

func generateInotifyReport(resultsDir string) {
	fmt.Println("### Config Notification Comparison")
	fmt.Println()

	inotifyDirs := findResultDirs(resultsDir, "inotify_clean")
	ipcDirs := findResultDirs(resultsDir, "ipc_clean")
	shmDirs := findResultDirs(resultsDir, "shm_clean")
	reliabilityDirs := findResultDirs(resultsDir, "inotify_reliability")

	if len(inotifyDirs) == 0 && len(ipcDirs) == 0 && len(shmDirs) == 0 {
		// Fall back to prefix-only matching for backwards compatibility
		inotifyDirs = findResultDirs(resultsDir, "inotify_")
		ipcDirs = findResultDirs(resultsDir, "ipc_")
	}

	if len(inotifyDirs) == 0 && len(ipcDirs) == 0 && len(shmDirs) == 0 {
		fmt.Println("No inotify, IPC, or SHM results found.")
		return
	}

	for _, dir := range inotifyDirs {
		printInotifyResults(dir)
	}

	for _, dir := range ipcDirs {
		printIPCResults(dir)
	}

	for _, dir := range shmDirs {
		printSHMResults(dir)
	}

	for _, dir := range reliabilityDirs {
		printReliabilityResults(dir)
	}
}

func findResultDirs(base, prefix string) []string {
	var dirs []string
	entries, err := os.ReadDir(base)
	if err != nil {
		return dirs
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			dirs = append(dirs, filepath.Join(base, e.Name()))
		}
	}
	sort.Strings(dirs)
	return dirs
}

func printInotifyResults(dir string) {
	fmt.Printf("#### inotify Results (%s)\n\n", filepath.Base(dir))

	fmt.Println("| Scenario | Events | Missed | Overflow | Coalesced | Dispatch p50 (ns) | Pipeline p50 (ns) | Pipeline p99 (ns) |")
	fmt.Println("|---|---|---|---|---|---|---|---|")

	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "watcher_") {
			return nil
		}
		var w InotifyWatcherResult
		if loadJSON(path, &w) != nil {
			return nil
		}
		scenario := strings.TrimPrefix(d.Name(), "watcher_")
		scenario = strings.TrimSuffix(scenario, ".json")
		fmt.Printf("| %s | %d | %d | %d | %d | %d | %d | %d |\n",
			scenario, w.TotalEvents, w.MissedEvents,
			w.OverflowEvents, w.CoalescedEvents,
			w.DispatchLatencyNs.P50,
			w.PipelineLatencyNs.P50, w.PipelineLatencyNs.P99)
		return nil
	})
	fmt.Println()
}

func printIPCResults(dir string) {
	fmt.Printf("#### IPC Socket Results (%s)\n\n", filepath.Base(dir))

	fmt.Println("| Scenario | Events | Dispatch p50 (ns) | Pipeline p50 (ns) | Pipeline p99 (ns) |")
	fmt.Println("|---|---|---|---|---|")

	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "server_") {
			return nil
		}
		var s IPCServerResult
		if loadJSON(path, &s) != nil {
			return nil
		}
		scenario := strings.TrimPrefix(d.Name(), "server_")
		scenario = strings.TrimSuffix(scenario, ".json")
		fmt.Printf("| %s | %d | %d | %d | %d |\n",
			scenario, s.TotalEvents,
			s.DispatchLatencyNs.P50,
			s.PipelineLatencyNs.P50, s.PipelineLatencyNs.P99)
		return nil
	})
	fmt.Println()
}

func printSHMResults(dir string) {
	fmt.Printf("#### Shared Memory Results (%s)\n\n", filepath.Base(dir))

	fmt.Println("| Scenario | Events | Seq Errors | Dispatch p50 (ns) | Pipeline p50 (ns) | Pipeline p99 (ns) |")
	fmt.Println("|---|---|---|---|---|---|")

	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "reader_") {
			return nil
		}
		var r SHMReaderResult
		if loadJSON(path, &r) != nil {
			return nil
		}
		scenario := strings.TrimPrefix(d.Name(), "reader_")
		scenario = strings.TrimSuffix(scenario, ".json")
		fmt.Printf("| %s | %d | %d | %d | %d | %d |\n",
			scenario, r.TotalEvents, r.SequenceErrors,
			r.DispatchLatencyNs.P50,
			r.PipelineLatencyNs.P50, r.PipelineLatencyNs.P99)
		return nil
	})
	fmt.Println()
}

func printReliabilityResults(dir string) {
	fmt.Printf("#### inotify Reliability (%s)\n\n", filepath.Base(dir))

	fmt.Println("| Scenario | Events | Missed | Overflow | Coalesced | Dispatch p50 (ns) | Pipeline p99 (ns) |")
	fmt.Println("|---|---|---|---|---|---|---|")

	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "watcher_") {
			return nil
		}
		var w InotifyWatcherResult
		if loadJSON(path, &w) != nil {
			return nil
		}
		scenario := strings.TrimPrefix(d.Name(), "watcher_")
		scenario = strings.TrimSuffix(scenario, ".json")
		fmt.Printf("| %s | %d | %d | %d | %d | %d | %d |\n",
			scenario, w.TotalEvents, w.MissedEvents,
			w.OverflowEvents, w.CoalescedEvents,
			w.DispatchLatencyNs.P50, w.PipelineLatencyNs.P99)
		return nil
	})
	fmt.Println()
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <results_dir>\n", os.Args[0])
		os.Exit(1)
	}

	resultsDir := os.Args[1]

	fmt.Println("# Integration Benchmark Results")
	fmt.Printf("## Results Directory: %s\n", resultsDir)
	fmt.Println()

	// Check for WAL results
	walDirs := findResultDirs(resultsDir, "wal_")
	for _, dir := range walDirs {
		fmt.Printf("## WAL Benchmark (%s)\n\n", filepath.Base(dir))
		generateWALReport(dir)
	}

	// Check for inotify/IPC results
	generateInotifyReport(resultsDir)

	fmt.Println("---")
	fmt.Println()
	fmt.Println("*Report generated by generate_report. No opinions — only measurements.*")
}
