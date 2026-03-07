package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/config"
)

func TestLoadResults(t *testing.T) {
	data, err := LoadResults("../../../example/results")
	if err != nil {
		t.Fatalf("LoadResults: %v", err)
	}

	if data.SystemInfo == "" {
		t.Error("expected system_info.txt to be loaded")
	}

	if len(data.WALClean) == 0 {
		t.Error("expected wal_clean scenarios")
	}

	// Verify known scenario exists
	found := false
	for _, s := range data.WALClean {
		if s.Name == "wal_33wps_1reader" {
			found = true
			if s.CWriter == nil {
				t.Error("wal_33wps_1reader: missing c_writer data")
			}
			if s.GoReader == nil {
				t.Error("wal_33wps_1reader: missing go_reader data")
			}
			// Verify known value
			writes := jsonInt(s.CWriter, "total_writes")
			if writes != 1960 {
				t.Errorf("total_writes: got %d, want 1960", writes)
			}
			break
		}
	}
	if !found {
		t.Error("wal_33wps_1reader scenario not found in wal_clean")
	}

	if len(data.InotifyClean) == 0 {
		t.Error("expected inotify_clean scenarios")
	}
	if len(data.IPCClean) == 0 {
		t.Error("expected ipc_clean scenarios")
	}
}

func TestLoadResults_WALSustained(t *testing.T) {
	data, err := LoadResults("../../../example/results")
	if err != nil {
		t.Fatalf("LoadResults: %v", err)
	}

	if data.WALSustained == nil {
		t.Fatal("expected wal_sustained data")
	}
	if data.WALSustained.CWriter == nil {
		t.Error("wal_sustained: missing c_writer")
	}
}

func TestLoadEventScenarios(t *testing.T) {
	events := loadEventScenarios("../../../example/results/inotify_clean")
	if len(events) != 3 {
		t.Fatalf("expected 3 inotify_clean scenarios, got %d", len(events))
	}

	// Check that 500ms scenario is loaded
	found := false
	for _, e := range events {
		if e.Scenario == "500ms" {
			found = true
			if e.Receiver == nil {
				t.Error("500ms: missing receiver data")
			}
			if e.Sender == nil {
				t.Error("500ms: missing sender data")
			}
			// Verify dispatch latency is in ns
			p99 := jsonLatency(e.Receiver, "dispatch_latency_ns", "p99")
			if p99 <= 0 {
				t.Error("500ms: dispatch_latency_ns.p99 should be > 0")
			}
			if p99 < 1000 {
				t.Errorf("500ms: dispatch_latency_ns.p99 = %d, looks like µs not ns", p99)
			}
			break
		}
	}
	if !found {
		t.Error("500ms scenario not found")
	}
}

func TestGenerateReport_AllPass(t *testing.T) {
	data, err := LoadResults("../../../example/results")
	if err != nil {
		t.Fatalf("LoadResults: %v", err)
	}

	// Use generous thresholds so everything passes
	cfg := &config.BenchConfig{}
	cfg.Thresholds.WAL.MaxBusyPct = 5.0
	cfg.Thresholds.WAL.MaxP99WriteLatencyUs = 50000
	cfg.Thresholds.WAL.MaxP99ReadLatencyUs = 50000
	cfg.Thresholds.Events.MaxP99DispatchLatencyUs = 50000
	cfg.Thresholds.Events.MaxMissedEventsPct = 10.0
	cfg.Thresholds.Sustained.MaxBusyPct = 5.0
	cfg.Thresholds.Sustained.MaxP99WriteLatencyUs = 50000

	var buf bytes.Buffer
	if err := Generate(&buf, data, cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	report := buf.String()

	// Must have the verdict header
	if !strings.Contains(report, "## Verdict: ALL PASS") {
		t.Error("expected ALL PASS verdict")
		// Print first 500 chars for debug
		if len(report) > 500 {
			t.Logf("report head: %s", report[:500])
		}
	}

	// Must have key sections
	for _, section := range []string{
		"Key Findings",
		"SQLite WAL Results",
		"inotify Sentinel",
		"Unix Domain Socket",
		"Appendix: Configured Thresholds",
	} {
		if !strings.Contains(report, section) {
			t.Errorf("missing section: %s", section)
		}
	}
}

func TestGenerateReport_WithFail(t *testing.T) {
	data, err := LoadResults("../../../example/results")
	if err != nil {
		t.Fatalf("LoadResults: %v", err)
	}

	// Set impossibly tight thresholds
	cfg := &config.BenchConfig{}
	cfg.Thresholds.WAL.MaxBusyPct = 0.0         // impossible — allow zero busy
	cfg.Thresholds.WAL.MaxP99WriteLatencyUs = 1 // 1µs — impossible
	cfg.Thresholds.Events.MaxP99DispatchLatencyUs = 1

	var buf bytes.Buffer
	if err := Generate(&buf, data, cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	report := buf.String()

	// Must have FAIL in the verdict
	if !strings.Contains(report, "FAIL") {
		t.Error("expected FAIL verdict with tight thresholds")
	}

	// Failures should appear in key findings
	if !strings.Contains(report, "exceeded their thresholds") {
		t.Error("expected threshold exceeded message in key findings")
	}
}

func TestGenerateReport_NoData(t *testing.T) {
	data := &ReportData{}
	cfg := config.Default()

	var buf bytes.Buffer
	if err := Generate(&buf, data, cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	report := buf.String()

	// Should produce a minimal report without crashing
	if !strings.Contains(report, "# Integration Benchmark Report") {
		t.Error("missing report header")
	}

	// Should NOT contain data sections
	if strings.Contains(report, "SQLite WAL Results") {
		t.Error("should not have WAL section with no data")
	}
}

func TestFmtNum(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
		{-1234, "-1,234"},
	}

	for _, tc := range tests {
		got := fmtNum(tc.in)
		if got != tc.want {
			t.Errorf("fmtNum(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestJsonHelpers(t *testing.T) {
	m := map[string]interface{}{
		"role":         "test",
		"total_writes": float64(1960),
		"write_latency_us": map[string]interface{}{
			"min": float64(185),
			"p99": float64(702),
		},
	}

	if got := jsonStr(m, "role"); got != "test" {
		t.Errorf("jsonStr: got %q, want %q", got, "test")
	}
	if got := jsonInt(m, "total_writes"); got != 1960 {
		t.Errorf("jsonInt: got %d, want 1960", got)
	}
	if got := jsonLatency(m, "write_latency_us", "p99"); got != 702 {
		t.Errorf("jsonLatency: got %d, want 702", got)
	}
	if got := jsonLatency(m, "nonexistent", "p99"); got != 0 {
		t.Errorf("jsonLatency missing: got %d, want 0", got)
	}
}

func TestScenarioSortKey(t *testing.T) {
	scenarios := []string{"1ms_burst", "100ms", "500ms"}
	expected := []string{"500ms", "100ms", "1ms_burst"}

	// Sort using our key
	sorted := make([]string, len(scenarios))
	copy(sorted, scenarios)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if scenarioSortKey(sorted[i]) > scenarioSortKey(sorted[j]) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	for i, got := range sorted {
		if got != expected[i] {
			t.Errorf("position %d: got %s, want %s", i, got, expected[i])
		}
	}
}

func TestComplexityScorecard_InReport(t *testing.T) {
	data, err := LoadResults("../../../example/results")
	if err != nil {
		t.Fatalf("LoadResults: %v", err)
	}

	cfg := &config.BenchConfig{}
	cfg.Thresholds.WAL.MaxBusyPct = 5.0
	cfg.Thresholds.WAL.MaxP99WriteLatencyUs = 50000
	cfg.Thresholds.WAL.MaxP99ReadLatencyUs = 50000
	cfg.Thresholds.Events.MaxP99DispatchLatencyUs = 50000
	cfg.Thresholds.Events.MaxMissedEventsPct = 10.0
	cfg.Thresholds.Sustained.MaxBusyPct = 5.0
	cfg.Thresholds.Sustained.MaxP99WriteLatencyUs = 50000

	var buf bytes.Buffer
	if err := Generate(&buf, data, cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	report := buf.String()

	// Scorecard section must appear
	if !strings.Contains(report, "Integration Complexity Scorecard") {
		t.Error("missing scorecard section")
	}

	// Must contain the signal table header
	if !strings.Contains(report, "Engineering Implication") {
		t.Error("missing scorecard table header")
	}

	// Must show triggered/clear counts
	if !strings.Contains(report, "signals triggered") {
		t.Error("missing triggered count summary")
	}
}

func TestCollectComplexitySignals(t *testing.T) {
	data, err := LoadResults("../../../example/results")
	if err != nil {
		t.Fatalf("LoadResults: %v", err)
	}

	signals := collectComplexitySignals(data)

	// Must have at least the WAL BUSY signal and inotify signals
	if len(signals) == 0 {
		t.Fatal("expected at least one complexity signal")
	}

	// Check signal names are present
	signalNames := make(map[string]bool)
	for _, s := range signals {
		signalNames[s.Signal] = true
	}

	for _, expected := range []string{
		"SQLITE_BUSY contention",
		"inotify missed events",
		"inotify event coalescing",
	} {
		if !signalNames[expected] {
			t.Errorf("missing expected signal: %s", expected)
		}
	}

	// Every signal must have non-empty fields
	for _, s := range signals {
		if s.Signal == "" {
			t.Error("signal has empty Signal field")
		}
		if s.Transport == "" {
			t.Errorf("signal %q has empty Transport", s.Signal)
		}
		if s.Measured == "" {
			t.Errorf("signal %q has empty Measured", s.Signal)
		}
		if s.Source == "" {
			t.Errorf("signal %q has empty Source", s.Signal)
		}
		if s.Implication == "" {
			t.Errorf("signal %q has empty Implication", s.Signal)
		}
	}
}

func TestComplexityScorecard_NoData(t *testing.T) {
	data := &ReportData{}

	var buf bytes.Buffer
	writeComplexityScorecard(&buf, data)

	if buf.Len() != 0 {
		t.Errorf("expected no output for empty data, got %d bytes", buf.Len())
	}
}

func TestDetectedTransports(t *testing.T) {
	data, err := LoadResults("../../../example/results")
	if err != nil {
		t.Fatalf("LoadResults: %v", err)
	}

	surfaces := DetectedTransports(data)

	// Must return all 4 transport types
	if len(surfaces) != 4 {
		t.Fatalf("expected 4 transport surfaces, got %d", len(surfaces))
	}

	// Example results have WAL and inotify and IPC data, but no SHM
	detectedNames := make(map[string]bool)
	for _, s := range surfaces {
		if s.Detected {
			detectedNames[s.Transport] = true
		}
		// Every surface must have non-empty fields
		if s.Interface == "" {
			t.Errorf("transport %q has empty Interface", s.Transport)
		}
		if s.AttackVector == "" {
			t.Errorf("transport %q has empty AttackVector", s.Transport)
		}
		if s.Boundary == "" {
			t.Errorf("transport %q has empty Boundary", s.Transport)
		}
		if s.DataFlow == "" {
			t.Errorf("transport %q has empty DataFlow", s.Transport)
		}
	}

	// At minimum, WAL and inotify should be detected from example data
	for _, expected := range []string{"SQLite WAL", "inotify sentinel file"} {
		if !detectedNames[expected] {
			t.Errorf("expected transport %q to be detected", expected)
		}
	}
}

func TestThreatModelSurface_InReport(t *testing.T) {
	data, err := LoadResults("../../../example/results")
	if err != nil {
		t.Fatalf("LoadResults: %v", err)
	}

	cfg := config.Default()

	var buf bytes.Buffer
	if err := Generate(&buf, data, cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	report := buf.String()

	if !strings.Contains(report, "Threat Model Surface Area") {
		t.Error("missing threat model section in report")
	}

	if !strings.Contains(report, "Trust Boundary") {
		t.Error("missing trust boundary column in threat model table")
	}

	if !strings.Contains(report, "Attack Vector") {
		t.Error("missing attack vector column in threat model table")
	}

	if !strings.Contains(report, "transport interface(s) detected") {
		t.Error("missing transport count summary")
	}
}

func TestThreatModelSurface_NoData(t *testing.T) {
	data := &ReportData{}

	var buf bytes.Buffer
	writeThreatModelSurface(&buf, data)

	if buf.Len() != 0 {
		t.Errorf("expected no output for empty data, got %d bytes", buf.Len())
	}
}
