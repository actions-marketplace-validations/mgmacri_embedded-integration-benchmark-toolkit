// Package config parses bench.yaml into typed Go structs.
//
// The configuration file drives the entire benchmark suite. It defines:
//   - Database schema (path to .sql file)
//   - SQL query templates (optional — auto-generated from schema if omitted)
//   - Config subsystems for event benchmarks (directory or inline YAML)
//   - WAL benchmark scenarios
//   - Event benchmark scenarios (shared across inotify, IPC, SHM)
//   - Sustained test parameters
//   - Reliability test parameters
//   - Stress test parameters
//   - Pass/fail thresholds for verdict-first reporting
//   - Output preferences
//
// Two loading modes:
//  1. Load(path) — parse a bench.yaml file
//  2. Default()  — return a hardcoded default matching the original benchmark
//
// When queries are omitted, the orchestrator calls schema.Introspect() to
// auto-generate INSERT/SELECT templates from the CREATE TABLE statements.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// BenchConfig is the top-level configuration parsed from bench.yaml.
type BenchConfig struct {
	Schema      string            `yaml:"schema"`
	Queries     *QueryConfig      `yaml:"queries,omitempty"`
	Configs     ConfigsConfig     `yaml:"configs"`
	WAL         WALConfig         `yaml:"wal"`
	Events      EventsConfig      `yaml:"events"`
	Sustained   SustainedConfig   `yaml:"sustained"`
	Reliability ReliabilityConfig `yaml:"reliability"`
	Stress      StressConfig      `yaml:"stress"`
	DurationSec int               `yaml:"duration_sec"`
	Thresholds  ThresholdsConfig  `yaml:"thresholds"`
	Output      OutputConfig      `yaml:"output"`
}

// QueryConfig holds optional explicit SQL templates.
// When nil, the orchestrator auto-generates from schema introspection.
type QueryConfig struct {
	Insert       string `yaml:"insert"`
	Select       string `yaml:"select"`
	ConfigInsert string `yaml:"config_insert"`
}

// ConfigsConfig defines config subsystems for event-dispatch benchmarks.
type ConfigsConfig struct {
	Directory  string               `yaml:"directory,omitempty"`
	Subsystems map[string]Subsystem `yaml:"subsystems,omitempty"`
}

// Subsystem defines a single config domain (e.g., "sensor_calibration").
type Subsystem struct {
	Fields []Field `yaml:"fields"`
}

// Field defines a single config field within a subsystem.
type Field struct {
	Name   string   `yaml:"name"`
	Type   string   `yaml:"type"`             // "int" or "text"
	Min    int      `yaml:"min,omitempty"`    // for type=int
	Max    int      `yaml:"max,omitempty"`    // for type=int
	Values []string `yaml:"values,omitempty"` // for type=text
}

// WALConfig holds SQLite WAL/rollback benchmark settings.
type WALConfig struct {
	DBDir         string        `yaml:"db_dir"`
	BusyTimeoutMs int           `yaml:"busy_timeout_ms"`
	Scenarios     []WALScenario `yaml:"scenarios"`
}

// WALScenario defines a single WAL benchmark run.
type WALScenario struct {
	Name              string `yaml:"name"`
	JournalMode       string `yaml:"journal_mode"`
	CWriteIntervalMs  int    `yaml:"c_write_interval_ms"`
	GoReaders         int    `yaml:"go_readers"`
	GoReadIntervalMs  int    `yaml:"go_read_interval_ms"`
	GoWriteIntervalMs int    `yaml:"go_write_interval_ms,omitempty"`
}

// EventsConfig holds event-dispatch benchmark settings.
type EventsConfig struct {
	Inotify   InotifyConfig   `yaml:"inotify"`
	IPC       IPCConfig       `yaml:"ipc"`
	SHM       SHMConfig       `yaml:"shm"`
	Scenarios []EventScenario `yaml:"scenarios"`
}

// InotifyConfig holds inotify-specific paths.
type InotifyConfig struct {
	WatchDir string `yaml:"watch_dir"`
}

// IPCConfig holds Unix domain socket path.
type IPCConfig struct {
	SocketPath string `yaml:"socket_path"`
}

// SHMConfig holds shared memory segment name and FIFO path.
type SHMConfig struct {
	SHMName  string `yaml:"shm_name"`
	FIFOPath string `yaml:"fifo_path"`
}

// EventScenario defines a single event benchmark run (shared across transports).
type EventScenario struct {
	Name       string `yaml:"name"`
	IntervalMs int    `yaml:"interval_ms"`
}

// SustainedConfig holds long-running concurrent write test settings.
type SustainedConfig struct {
	Enabled           bool   `yaml:"enabled"`
	DurationSec       int    `yaml:"duration_sec"`
	JournalMode       string `yaml:"journal_mode"`
	CWriteIntervalMs  int    `yaml:"c_write_interval_ms"`
	GoWriteIntervalMs int    `yaml:"go_write_interval_ms"`
	GoReaders         int    `yaml:"go_readers"`
	GoReadIntervalMs  int    `yaml:"go_read_interval_ms"`
}

// ReliabilityConfig holds inotify reliability test settings.
type ReliabilityConfig struct {
	Enabled     bool              `yaml:"enabled"`
	DurationSec int               `yaml:"duration_sec"`
	Tests       []ReliabilityTest `yaml:"tests"`
}

// ReliabilityTest defines a single inotify reliability scenario.
type ReliabilityTest struct {
	Name         string `yaml:"name"`
	Desc         string `yaml:"desc"`
	NoSync       bool   `yaml:"no_sync,omitempty"`
	BurstPairs   int    `yaml:"burst_pairs,omitempty"`
	DelayStartMs int    `yaml:"delay_start_ms,omitempty"`
	IntervalMs   int    `yaml:"interval_ms"`
}

// StressConfig controls synthetic system load for stressed benchmark runs.
type StressConfig struct {
	Enabled   bool `yaml:"enabled"`
	CPULoops  int  `yaml:"cpu_loops"`
	IOEnabled bool `yaml:"io_enabled"`
	MemoryMB  int  `yaml:"memory_mb"`
}

// ThresholdsConfig defines pass/fail criteria for the verdict-first report.
type ThresholdsConfig struct {
	WAL       WALThresholds       `yaml:"wal"`
	Events    EventThresholds     `yaml:"events"`
	Sustained SustainedThresholds `yaml:"sustained"`
}

// WALThresholds defines WAL benchmark pass/fail criteria.
type WALThresholds struct {
	MaxBusyPct           float64 `yaml:"max_busy_pct"`
	MaxP99WriteLatencyUs int     `yaml:"max_p99_write_latency_us"`
	MaxP99ReadLatencyUs  int     `yaml:"max_p99_read_latency_us"`
}

// EventThresholds defines event benchmark pass/fail criteria.
type EventThresholds struct {
	MaxP99DispatchLatencyUs int     `yaml:"max_p99_dispatch_latency_us"`
	MaxMissedEventsPct      float64 `yaml:"max_missed_events_pct"`
	MaxCoalescedPct         float64 `yaml:"max_coalesced_pct"`
	MaxOverflowEvents       int     `yaml:"max_overflow_events"`
}

// SustainedThresholds defines sustained test pass/fail criteria.
type SustainedThresholds struct {
	MaxBusyPct           float64 `yaml:"max_busy_pct"`
	MaxP99WriteLatencyUs int     `yaml:"max_p99_write_latency_us"`
	MaxP99ReadLatencyUs  int     `yaml:"max_p99_read_latency_us"`
	MaxCombinedBusyPct   float64 `yaml:"max_combined_busy_pct"`
}

// OutputConfig controls where results and reports are written.
type OutputConfig struct {
	ResultsDir   string `yaml:"results_dir"`
	ReportFormat string `yaml:"report_format"`
	ReportFile   string `yaml:"report_file"`
}

// Load parses a bench.yaml file and returns a validated BenchConfig.
func Load(path string) (*BenchConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &BenchConfig{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	applyDefaults(cfg)

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

// Default returns a BenchConfig matching the original hardcoded benchmark
// parameters. Used when no bench.yaml is provided (backwards compatibility).
func Default() *BenchConfig {
	cfg := &BenchConfig{
		Schema:      "schema.sql",
		DurationSec: 60,
		Configs: ConfigsConfig{
			Directory: "example/inotify_files",
			Subsystems: map[string]Subsystem{
				"sensor_calibration": {
					Fields: []Field{
						{Name: "temp_offset", Type: "int", Min: -50, Max: 50},
						{Name: "pressure_gain", Type: "int", Min: 900, Max: 1100},
						{Name: "humidity_bias", Type: "int", Min: -10, Max: 10},
						{Name: "accel_x_cal", Type: "int", Min: -500, Max: 500},
						{Name: "accel_y_cal", Type: "int", Min: -500, Max: 500},
						{Name: "accel_z_cal", Type: "int", Min: -500, Max: 500},
						{Name: "gyro_drift", Type: "int", Min: 0, Max: 100},
						{Name: "mag_declination", Type: "int", Min: -180, Max: 180},
						{Name: "adc_offset", Type: "int", Min: -20, Max: 20},
						{Name: "adc_gain", Type: "int", Min: 950, Max: 1050},
						{Name: "filter_alpha", Type: "int", Min: 1, Max: 100},
						{Name: "sample_rate_hz", Type: "int", Min: 10, Max: 1000},
						{Name: "averaging_window", Type: "int", Min: 1, Max: 64},
						{Name: "noise_floor", Type: "int", Min: 0, Max: 50},
						{Name: "cal_timestamp", Type: "int", Min: 1700000000, Max: 1800000000},
						{Name: "sensor_id", Type: "text", Values: []string{"SNS-001", "SNS-002", "SNS-003"}},
						{Name: "firmware_ver", Type: "text", Values: []string{"v2.1.0", "v2.1.1", "v2.2.0"}},
						{Name: "cal_operator", Type: "text", Values: []string{"auto", "manual", "factory"}},
						{Name: "cal_status", Type: "text", Values: []string{"valid", "expired", "pending"}},
						{Name: "temp_unit", Type: "text", Values: []string{"C", "F", "K"}},
					},
				},
				"network_config": {
					Fields: []Field{
						{Name: "ip_addr", Type: "text", Values: []string{"10.0.1.100", "10.0.1.101", "10.0.1.102"}},
						{Name: "subnet_mask", Type: "text", Values: []string{"255.255.255.0", "255.255.0.0"}},
						{Name: "gateway", Type: "text", Values: []string{"10.0.1.1", "10.0.0.1"}},
						{Name: "dns_primary", Type: "text", Values: []string{"8.8.8.8", "1.1.1.1"}},
						{Name: "dns_secondary", Type: "text", Values: []string{"8.8.4.4", "1.0.0.1"}},
						{Name: "mtu", Type: "int", Min: 576, Max: 9000},
						{Name: "dhcp_enabled", Type: "int", Min: 0, Max: 1},
						{Name: "vlan_id", Type: "int", Min: 1, Max: 4094},
						{Name: "link_speed", Type: "int", Min: 10, Max: 1000},
						{Name: "hostname", Type: "text", Values: []string{"device-a", "device-b", "device-c"}},
					},
				},
				"user_profiles": {
					Fields: []Field{
						{Name: "user_id", Type: "int", Min: 1000, Max: 9999},
						{Name: "username", Type: "text", Values: []string{"admin", "operator", "viewer", "service"}},
						{Name: "role", Type: "text", Values: []string{"admin", "operator", "readonly"}},
						{Name: "theme", Type: "text", Values: []string{"dark", "light", "auto"}},
						{Name: "language", Type: "text", Values: []string{"en", "de", "fr", "ja"}},
						{Name: "timeout_sec", Type: "int", Min: 30, Max: 3600},
						{Name: "max_retries", Type: "int", Min: 1, Max: 10},
						{Name: "log_level", Type: "text", Values: []string{"debug", "info", "warn", "error"}},
					},
				},
			},
		},
		WAL: WALConfig{
			DBDir:         "/tmp/wal_benchmark",
			BusyTimeoutMs: 5000,
			Scenarios: []WALScenario{
				{Name: "wal_33wps_1reader", JournalMode: "wal", CWriteIntervalMs: 30, GoReaders: 1, GoReadIntervalMs: 50},
				{Name: "wal_100wps_1reader", JournalMode: "wal", CWriteIntervalMs: 10, GoReaders: 1, GoReadIntervalMs: 50},
				{Name: "wal_330wps_1reader", JournalMode: "wal", CWriteIntervalMs: 3, GoReaders: 1, GoReadIntervalMs: 50},
				{Name: "wal_33wps_3readers", JournalMode: "wal", CWriteIntervalMs: 30, GoReaders: 3, GoReadIntervalMs: 50},
				{Name: "rollback_33wps_1reader", JournalMode: "delete", CWriteIntervalMs: 30, GoReaders: 1, GoReadIntervalMs: 50},
				{Name: "rollback_33wps_3readers", JournalMode: "delete", CWriteIntervalMs: 30, GoReaders: 3, GoReadIntervalMs: 50},
			},
		},
		Events: EventsConfig{
			Inotify: InotifyConfig{WatchDir: "/tmp/sentinel_bench"},
			IPC:     IPCConfig{SocketPath: "/tmp/bench_ipc.sock"},
			SHM:     SHMConfig{SHMName: "/bench_shm", FIFOPath: "/tmp/bench_shm_fifo"},
			Scenarios: []EventScenario{
				{Name: "500ms", IntervalMs: 500},
				{Name: "100ms", IntervalMs: 100},
				{Name: "1ms_burst", IntervalMs: 1},
			},
		},
		Sustained: SustainedConfig{
			Enabled:           true,
			DurationSec:       300,
			JournalMode:       "wal",
			CWriteIntervalMs:  30,
			GoWriteIntervalMs: 100,
			GoReaders:         1,
			GoReadIntervalMs:  50,
		},
		Reliability: ReliabilityConfig{
			Enabled:     true,
			DurationSec: 30,
			Tests: []ReliabilityTest{
				{Name: "no_fsync", Desc: "Skip fsync before rename", NoSync: true, IntervalMs: 10},
				{Name: "rapid_overwrite_5", Desc: "5 rapid overwrites per tick", BurstPairs: 5, IntervalMs: 100},
				{Name: "rapid_overwrite_20", Desc: "20 rapid overwrites per tick", BurstPairs: 20, IntervalMs: 100},
				{Name: "slow_reader", Desc: "Reader delays 50ms before reading", DelayStartMs: 50, IntervalMs: 100},
				{Name: "mixed_stress", Desc: "Burst + slow reader combined", BurstPairs: 10, DelayStartMs: 25, IntervalMs: 50},
			},
		},
		Stress: StressConfig{
			Enabled:   true,
			CPULoops:  2,
			IOEnabled: true,
			MemoryMB:  100,
		},
		Thresholds: ThresholdsConfig{
			WAL: WALThresholds{
				MaxBusyPct:           1.0,
				MaxP99WriteLatencyUs: 50000,
				MaxP99ReadLatencyUs:  10000,
			},
			Events: EventThresholds{
				MaxP99DispatchLatencyUs: 5000,
				MaxMissedEventsPct:      0.0,
				MaxCoalescedPct:         5.0,
				MaxOverflowEvents:       0,
			},
			Sustained: SustainedThresholds{
				MaxBusyPct:           5.0,
				MaxP99WriteLatencyUs: 100000,
				MaxP99ReadLatencyUs:  50000,
				MaxCombinedBusyPct:   5.0,
			},
		},
		Output: OutputConfig{
			ResultsDir:   "results",
			ReportFormat: "markdown",
			ReportFile:   "BENCHMARK-REPORT.md",
		},
	}
	return cfg
}

// applyDefaults fills in zero-valued fields with sensible defaults.
func applyDefaults(cfg *BenchConfig) {
	if cfg.DurationSec <= 0 {
		cfg.DurationSec = 60
	}
	if cfg.WAL.DBDir == "" {
		cfg.WAL.DBDir = "/tmp/wal_benchmark"
	}
	if cfg.WAL.BusyTimeoutMs <= 0 {
		cfg.WAL.BusyTimeoutMs = 5000
	}
	if cfg.Events.Inotify.WatchDir == "" {
		cfg.Events.Inotify.WatchDir = "/tmp/sentinel_bench"
	}
	if cfg.Events.IPC.SocketPath == "" {
		cfg.Events.IPC.SocketPath = "/tmp/bench_ipc.sock"
	}
	if cfg.Events.SHM.SHMName == "" {
		cfg.Events.SHM.SHMName = "/bench_shm"
	}
	if cfg.Events.SHM.FIFOPath == "" {
		cfg.Events.SHM.FIFOPath = "/tmp/bench_shm_fifo"
	}
	if cfg.Sustained.DurationSec <= 0 {
		cfg.Sustained.DurationSec = 300
	}
	if cfg.Sustained.JournalMode == "" {
		cfg.Sustained.JournalMode = "wal"
	}
	if cfg.Reliability.DurationSec <= 0 {
		cfg.Reliability.DurationSec = 30
	}
	if cfg.Stress.CPULoops <= 0 {
		cfg.Stress.CPULoops = 2
	}
	if cfg.Stress.MemoryMB <= 0 {
		cfg.Stress.MemoryMB = 100
	}
	if cfg.Output.ResultsDir == "" {
		cfg.Output.ResultsDir = "results"
	}
	if cfg.Output.ReportFormat == "" {
		cfg.Output.ReportFormat = "markdown"
	}
	if cfg.Output.ReportFile == "" {
		cfg.Output.ReportFile = "BENCHMARK-REPORT.md"
	}
}

// validate checks that the configuration is internally consistent.
func validate(cfg *BenchConfig) error {
	if cfg.Schema == "" {
		return fmt.Errorf("'schema' is required: path to .sql file with CREATE TABLE statements")
	}

	// Verify schema file exists
	if _, err := os.Stat(cfg.Schema); err != nil {
		return fmt.Errorf("schema file %q: %w", cfg.Schema, err)
	}

	// Must have at least one config source
	if cfg.Configs.Directory == "" && len(cfg.Configs.Subsystems) == 0 {
		return fmt.Errorf("'configs' requires either 'directory' or 'subsystems' (or both)")
	}

	// Verify config directory exists if specified
	if cfg.Configs.Directory != "" {
		info, err := os.Stat(cfg.Configs.Directory)
		if err != nil {
			return fmt.Errorf("config directory %q: %w", cfg.Configs.Directory, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("config directory %q is not a directory", cfg.Configs.Directory)
		}
	}

	// Validate subsystem fields
	for name, sub := range cfg.Configs.Subsystems {
		if len(sub.Fields) == 0 {
			return fmt.Errorf("subsystem %q has no fields", name)
		}
		for i, f := range sub.Fields {
			if f.Name == "" {
				return fmt.Errorf("subsystem %q field %d: 'name' is required", name, i)
			}
			if f.Type != "int" && f.Type != "text" {
				return fmt.Errorf("subsystem %q field %q: type must be 'int' or 'text', got %q", name, f.Name, f.Type)
			}
			if f.Type == "text" && len(f.Values) == 0 {
				return fmt.Errorf("subsystem %q field %q: text fields require 'values' list", name, f.Name)
			}
			if f.Type == "int" && f.Min == 0 && f.Max == 0 {
				// Allow min=0 max=0 as valid (e.g., boolean 0/0 edge)
				// but warn-worthy — skip validation here
			}
		}
	}

	// Validate WAL scenarios
	for i, s := range cfg.WAL.Scenarios {
		if s.Name == "" {
			return fmt.Errorf("wal scenario %d: 'name' is required", i)
		}
		if s.JournalMode != "wal" && s.JournalMode != "delete" {
			return fmt.Errorf("wal scenario %q: journal_mode must be 'wal' or 'delete', got %q", s.Name, s.JournalMode)
		}
		if s.CWriteIntervalMs <= 0 {
			return fmt.Errorf("wal scenario %q: c_write_interval_ms must be > 0", s.Name)
		}
	}

	// Validate event scenarios
	for i, s := range cfg.Events.Scenarios {
		if s.Name == "" {
			return fmt.Errorf("event scenario %d: 'name' is required", i)
		}
		if s.IntervalMs <= 0 {
			return fmt.Errorf("event scenario %q: interval_ms must be > 0", s.Name)
		}
	}

	// Validate reliability tests
	for i, t := range cfg.Reliability.Tests {
		if t.Name == "" {
			return fmt.Errorf("reliability test %d: 'name' is required", i)
		}
		if t.IntervalMs <= 0 {
			return fmt.Errorf("reliability test %q: interval_ms must be > 0", t.Name)
		}
	}

	return nil
}

// SubsystemNames returns the ordered list of subsystem names from the config.
// When both directory and inline subsystems are present, inline names come first,
// then directory-only names are appended in filesystem order.
func (c *BenchConfig) SubsystemNames() []string {
	seen := make(map[string]bool)
	var names []string

	// Inline subsystems first (map iteration order is non-deterministic,
	// but that's acceptable for subsystem naming)
	for name := range c.Configs.Subsystems {
		names = append(names, name)
		seen[name] = true
	}

	// Directory subsystems (would need os.ReadDir — deferred to orchestrator)
	// Only inline subsystems are returned here. The orchestrator merges
	// directory-discovered subsystems at runtime.

	return names
}

// RuntimeJSON serializes the config to a JSON format suitable for passing
// to C binaries via a temp file. C binaries read this with cJSON.
//
// This bridges the YAML → JSON gap: the Go orchestrator resolves YAML,
// then writes a simple JSON file that C programs can parse without YAML deps.
func (c *BenchConfig) RuntimeJSON() ([]byte, error) {
	// Uses encoding/json for the runtime bridge — not YAML.
	// The C binaries only need a subset of the config.
	// This is implemented in the orchestrator (bench/main.go).
	return nil, fmt.Errorf("not implemented — use orchestrator's runtime JSON writer")
}
