// Package main implements the `bench` CLI — the single entry point for the
// integration benchmark toolkit.
//
// Commands:
//
//	bench init                      Generate a starter bench.yaml interactively
//	bench run  [bench.yaml] [flags] Run benchmarks defined in config
//	bench report [results_dir]      Generate verdict-first report from results
//
// The `bench run` command:
//  1. Parses bench.yaml (or uses built-in defaults)
//  2. Introspects the SQL schema to auto-generate INSERT/SELECT statements
//  3. Writes a runtime JSON config for C binaries (/tmp/bench_runtime.json)
//  4. Orchestrates all benchmark phases (WAL, events, sustained, reliability, stress)
//  5. Collects results into timestamped directories
//
// This replaces the shell-script orchestrators (run_all.sh, run_wal_benchmark.sh,
// etc.) with a single Go binary that reads bench.yaml and drives everything.
// The shell scripts remain for backwards compatibility.
//
// Build: CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -o build/bench ./benchmark/bench
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/config"
	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/report"
	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/schema"
)

const usage = `bench — Integration Benchmark Toolkit

Usage:
  bench init                       Generate a starter bench.yaml
  bench run  [config.yaml] [opts]  Run benchmarks
  bench report [results_dir]       Generate report from results

Run options:
  --only <section>   Run only: wal, events, sustained, reliability, stress
  --duration <sec>   Override per-scenario duration
  --dry-run          Print what would run without executing

Report options:
  --config <path>    Path to bench.yaml for thresholds
  --output <path>    Write report to file instead of stdout
  --gate             Exit non-zero if any verdict exceeds thresholds (CI mode)

Environment:
  BENCH_CONFIG       Path to bench.yaml (default: bench.yaml)
  BENCH_RESULTS      Base results directory (default: results)

Examples:
  bench run bench.yaml
  bench run bench.yaml --only wal --duration 30
  bench run                        # uses defaults (no config file needed)
  bench report results/final_20250101_120000/
  bench report results/ --gate     # fail CI if thresholds exceeded
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "init":
		cmdInit()
	case "run":
		cmdRun(os.Args[2:])
	case "report":
		cmdReport(os.Args[2:])
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", cmd, usage)
		os.Exit(1)
	}
}

// cmdInit generates a starter bench.yaml in the current directory.
func cmdInit() {
	outPath := "bench.yaml"
	if _, err := os.Stat(outPath); err == nil {
		fmt.Fprintf(os.Stderr, "error: %s already exists\n", outPath)
		os.Exit(1)
	}

	// Read the example config and write it
	examplePath := "bench.example.yaml"
	if data, err := os.ReadFile(examplePath); err == nil {
		if err := os.WriteFile(outPath, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "error writing %s: %v\n", outPath, err)
			os.Exit(1)
		}
		fmt.Printf("Created %s (copied from %s)\n", outPath, examplePath)
		fmt.Println("Edit the file to match your schema and config domains, then run: bench run bench.yaml")
		return
	}

	// Fallback: generate minimal config
	cfg := config.Default()
	data := generateMinimalYAML(cfg)
	if err := os.WriteFile(outPath, []byte(data), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing %s: %v\n", outPath, err)
		os.Exit(1)
	}
	fmt.Printf("Created %s with default settings\n", outPath)
	fmt.Println("Edit the file to match your schema and config domains, then run: bench run bench.yaml")
}

// cmdRun orchestrates the benchmark suite.
func cmdRun(args []string) {
	// Parse flags
	var configPath string
	var only string
	var durationOverride int
	var dryRun bool

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--only":
			if i+1 < len(args) {
				only = args[i+1]
				i++
			}
		case "--duration":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &durationOverride)
				i++
			}
		case "--dry-run":
			dryRun = true
		default:
			if !strings.HasPrefix(args[i], "-") && configPath == "" {
				configPath = args[i]
			}
		}
	}

	// Load configuration
	var cfg *config.BenchConfig
	var err error

	if configPath == "" {
		// Check env var
		if envPath := os.Getenv("BENCH_CONFIG"); envPath != "" {
			configPath = envPath
		} else if _, err := os.Stat("bench.yaml"); err == nil {
			configPath = "bench.yaml"
		}
	}

	if configPath != "" {
		cfg, err = config.Load(configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "[bench] Loaded config: %s\n", configPath)
	} else {
		cfg = config.Default()
		fmt.Fprintf(os.Stderr, "[bench] Using built-in default config\n")
	}

	// Apply overrides
	if durationOverride > 0 {
		cfg.DurationSec = durationOverride
	}

	// Introspect schema
	tables, err := schema.Introspect(cfg.Schema)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: schema introspection failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "[bench] Schema: %s — %d table(s) found\n", cfg.Schema, len(tables))
	for _, t := range tables {
		fmt.Fprintf(os.Stderr, "[bench]   %s: %d columns, INSERT has %d params\n",
			t.Name, len(t.Columns), t.SQL.ColumnCount)
	}

	// Determine SQL templates: explicit from config, or auto-generated from schema
	dataTable := schema.FindTable(tables, "sample_data")
	if dataTable == nil && len(tables) > 0 {
		dataTable = &tables[0] // Use first table if sample_data not found
	}

	insertSQL := ""
	selectSQL := ""
	if cfg.Queries != nil && cfg.Queries.Insert != "" {
		insertSQL = strings.TrimSpace(cfg.Queries.Insert)
		fmt.Fprintf(os.Stderr, "[bench] Using explicit INSERT from config\n")
	} else if dataTable != nil {
		insertSQL = dataTable.SQL.Insert
		fmt.Fprintf(os.Stderr, "[bench] Auto-generated INSERT: %d columns\n", dataTable.SQL.ColumnCount)
	}

	if cfg.Queries != nil && cfg.Queries.Select != "" {
		selectSQL = strings.TrimSpace(cfg.Queries.Select)
		fmt.Fprintf(os.Stderr, "[bench] Using explicit SELECT from config\n")
	} else if dataTable != nil {
		selectSQL = dataTable.SQL.SelectWhere
		fmt.Fprintf(os.Stderr, "[bench] Auto-generated SELECT with WHERE clause\n")
	}

	// Write runtime JSON for C binaries
	runtimeCfg := buildRuntimeJSON(cfg, tables, insertSQL, selectSQL)
	runtimePath := "/tmp/bench_runtime.json"
	if err := writeRuntimeJSON(runtimeCfg, runtimePath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write runtime config: %v\n", err)
		// Non-fatal: C binaries will fall back to defaults
	} else {
		fmt.Fprintf(os.Stderr, "[bench] Wrote runtime config: %s\n", runtimePath)
	}

	// Dry run: print configuration summary and exit
	if dryRun {
		printDryRun(cfg, tables, only)
		return
	}

	// Execute benchmark phases
	fmt.Fprintf(os.Stderr, "\n[bench] Starting benchmark suite (duration: %ds per scenario)\n", cfg.DurationSec)

	sections := []string{"wal", "events", "sustained", "reliability", "stress"}
	if only != "" {
		sections = []string{only}
	}

	for _, section := range sections {
		switch section {
		case "wal":
			if len(cfg.WAL.Scenarios) > 0 {
				fmt.Fprintf(os.Stderr, "\n[bench] === WAL Benchmark (%d scenarios) ===\n", len(cfg.WAL.Scenarios))
				// TODO: Execute WAL scenarios via process spawning
				fmt.Fprintf(os.Stderr, "[bench] WAL execution: planned — use run_wal_benchmark.sh for now\n")
			}
		case "events":
			if len(cfg.Events.Scenarios) > 0 {
				fmt.Fprintf(os.Stderr, "\n[bench] === Event Benchmarks (%d scenarios × 3 transports) ===\n", len(cfg.Events.Scenarios))
				fmt.Fprintf(os.Stderr, "[bench] Event execution: planned — use individual run scripts for now\n")
			}
		case "sustained":
			if cfg.Sustained.Enabled {
				fmt.Fprintf(os.Stderr, "\n[bench] === Sustained WAL Test (%ds) ===\n", cfg.Sustained.DurationSec)
				fmt.Fprintf(os.Stderr, "[bench] Sustained execution: planned\n")
			}
		case "reliability":
			if cfg.Reliability.Enabled && len(cfg.Reliability.Tests) > 0 {
				fmt.Fprintf(os.Stderr, "\n[bench] === Reliability Tests (%d tests) ===\n", len(cfg.Reliability.Tests))
				fmt.Fprintf(os.Stderr, "[bench] Reliability execution: planned\n")
			}
		case "stress":
			if cfg.Stress.Enabled {
				fmt.Fprintf(os.Stderr, "\n[bench] === Stressed Re-run ===\n")
				fmt.Fprintf(os.Stderr, "[bench] Stress execution: planned\n")
			}
		default:
			fmt.Fprintf(os.Stderr, "[bench] Unknown section: %s (valid: wal, events, sustained, reliability, stress)\n", section)
		}
	}

	fmt.Fprintf(os.Stderr, "\n[bench] Suite complete.\n")
	fmt.Fprintf(os.Stderr, "[bench] To generate report: bench report %s/\n", cfg.Output.ResultsDir)
}

// cmdReport generates a verdict-first benchmark report.
func cmdReport(args []string) {
	resultsDir := "results"
	configPath := ""
	outputFile := ""
	gate := false

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config" && i+1 < len(args):
			i++
			configPath = args[i]
		case args[i] == "--output" || args[i] == "-o":
			if i+1 < len(args) {
				i++
				outputFile = args[i]
			}
		case args[i] == "--gate":
			gate = true
		case !strings.HasPrefix(args[i], "-"):
			resultsDir = args[i]
		default:
			fmt.Fprintf(os.Stderr, "error: unknown flag %q\n", args[i])
			os.Exit(1)
		}
	}

	if _, err := os.Stat(resultsDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: results directory %q not found\n", resultsDir)
		os.Exit(1)
	}

	// Load config for thresholds
	cfg := loadConfigBestEffort(configPath)

	fmt.Fprintf(os.Stderr, "[bench] Loading results from %s\n", resultsDir)
	data, err := report.LoadResults(resultsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading results: %v\n", err)
		os.Exit(1)
	}

	// Generate report
	var w *os.File
	if outputFile != "" {
		w, err = os.Create(outputFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: creating output file: %v\n", err)
			os.Exit(1)
		}
		defer w.Close()
	} else {
		w = os.Stdout
	}

	if err := report.Generate(w, data, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: generating report: %v\n", err)
		os.Exit(1)
	}

	if outputFile != "" {
		fmt.Fprintf(os.Stderr, "[bench] Report written to %s\n", outputFile)
	}

	// CI gating: exit non-zero if any verdict fails
	if gate {
		verdicts := report.Evaluate(data, cfg)
		code := report.ExitCode(verdicts)
		if code != 0 {
			failCount := 0
			for _, v := range verdicts {
				if !v.Pass {
					failCount++
				}
			}
			fmt.Fprintf(os.Stderr, "[bench] GATE FAILED: %d of %d criteria exceeded thresholds\n", failCount, len(verdicts))
		} else {
			fmt.Fprintf(os.Stderr, "[bench] GATE PASSED: all %d criteria within thresholds\n", len(verdicts))
		}
		os.Exit(code)
	}
}

// loadConfigBestEffort loads a config from the given path, or tries standard
// locations, or returns the built-in defaults. Never fails.
func loadConfigBestEffort(path string) *config.BenchConfig {
	if path != "" {
		cfg, err := config.Load(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[bench] warning: could not load config %q: %v (using defaults)\n", path, err)
			return config.Default()
		}
		return cfg
	}

	// Try standard locations
	for _, candidate := range []string{"bench.yaml", "bench.yml"} {
		if _, err := os.Stat(candidate); err == nil {
			cfg, err := config.Load(candidate)
			if err == nil {
				fmt.Fprintf(os.Stderr, "[bench] Using config from %s\n", candidate)
				return cfg
			}
		}
	}

	if envPath := os.Getenv("BENCH_CONFIG"); envPath != "" {
		cfg, err := config.Load(envPath)
		if err == nil {
			return cfg
		}
	}

	return config.Default()
}

// RuntimeConfig is the JSON structure written for C binaries.
type RuntimeConfig struct {
	Schema     RuntimeSchema      `json:"schema"`
	Subsystems []RuntimeSubsystem `json:"subsystems"`
	Paths      RuntimePaths       `json:"paths"`
}

type RuntimeSchema struct {
	Table       string          `json:"table"`
	InsertSQL   string          `json:"insert_sql"`
	SelectSQL   string          `json:"select_sql"`
	ColumnCount int             `json:"column_count"`
	Columns     []RuntimeColumn `json:"columns"`
}

type RuntimeColumn struct {
	Name     string `json:"name"`
	Affinity string `json:"affinity"`
	Hint     string `json:"hint"`
}

type RuntimeSubsystem struct {
	Name       string         `json:"name"`
	FieldCount int            `json:"field_count"`
	Fields     []RuntimeField `json:"fields"`
}

type RuntimeField struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	MinVal int      `json:"min,omitempty"`
	MaxVal int      `json:"max,omitempty"`
	Values []string `json:"values,omitempty"`
}

type RuntimePaths struct {
	WatchDir   string `json:"watch_dir"`
	SocketPath string `json:"socket_path"`
	SHMName    string `json:"shm_name"`
	FIFOPath   string `json:"fifo_path"`
}

func buildRuntimeJSON(cfg *config.BenchConfig, tables []schema.Table, insertSQL, selectSQL string) RuntimeConfig {
	rt := RuntimeConfig{
		Paths: RuntimePaths{
			WatchDir:   cfg.Events.Inotify.WatchDir,
			SocketPath: cfg.Events.IPC.SocketPath,
			SHMName:    cfg.Events.SHM.SHMName,
			FIFOPath:   cfg.Events.SHM.FIFOPath,
		},
	}

	// Schema
	if len(tables) > 0 {
		t := tables[0]
		for _, tab := range tables {
			if strings.EqualFold(tab.Name, "sample_data") {
				t = tab
				break
			}
		}

		var cols []RuntimeColumn
		for _, c := range t.Columns {
			if !c.IsAutoInc {
				hint := inferHint(c.Name)
				cols = append(cols, RuntimeColumn{
					Name:     c.Name,
					Affinity: c.Affinity,
					Hint:     hint,
				})
			}
		}

		rt.Schema = RuntimeSchema{
			Table:       t.Name,
			InsertSQL:   insertSQL,
			SelectSQL:   selectSQL,
			ColumnCount: len(cols),
			Columns:     cols,
		}
	}

	// Subsystems
	for name, sub := range cfg.Configs.Subsystems {
		rtSub := RuntimeSubsystem{
			Name:       name,
			FieldCount: len(sub.Fields),
		}
		for _, f := range sub.Fields {
			rtSub.Fields = append(rtSub.Fields, RuntimeField{
				Name:   f.Name,
				Type:   f.Type,
				MinVal: f.Min,
				MaxVal: f.Max,
				Values: f.Values,
			})
		}
		rt.Subsystems = append(rt.Subsystems, rtSub)
	}

	return rt
}

func inferHint(name string) string {
	lower := strings.ToLower(name)
	switch {
	case lower == "date" || strings.HasSuffix(lower, "_date"):
		return "date"
	case lower == "time" || strings.HasSuffix(lower, "_time"):
		return "time"
	case strings.Contains(lower, "flag") || strings.Contains(lower, "status"):
		return "flag"
	case strings.Contains(lower, "serial") || strings.Contains(lower, "device"):
		return "serial"
	case strings.Contains(lower, "label") || strings.Contains(lower, "source"):
		return "label"
	case strings.Contains(lower, "name") || strings.Contains(lower, "operator"):
		return "name"
	case strings.Contains(lower, "id"):
		return "id"
	default:
		return ""
	}
}

func writeRuntimeJSON(cfg RuntimeConfig, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime config: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

func printDryRun(cfg *config.BenchConfig, tables []schema.Table, only string) {
	fmt.Println("\n=== DRY RUN — Benchmark Plan ===")
	fmt.Printf("Duration: %d seconds per scenario\n", cfg.DurationSec)
	fmt.Printf("Schema:   %s (%d tables)\n", cfg.Schema, len(tables))
	for _, t := range tables {
		fmt.Printf("  Table %s: %d columns\n", t.Name, len(t.Columns))
	}
	fmt.Printf("Subsystems: %d\n", len(cfg.Configs.Subsystems))
	for name, sub := range cfg.Configs.Subsystems {
		fmt.Printf("  %s: %d fields\n", name, len(sub.Fields))
	}

	showSection := func(name string) bool {
		return only == "" || only == name
	}

	if showSection("wal") && len(cfg.WAL.Scenarios) > 0 {
		fmt.Printf("\nWAL Scenarios (%d):\n", len(cfg.WAL.Scenarios))
		for _, s := range cfg.WAL.Scenarios {
			readers := s.GoReaders
			if readers == 0 {
				readers = 1
			}
			fmt.Printf("  %s: journal=%s c_interval=%dms readers=%d\n",
				s.Name, s.JournalMode, s.CWriteIntervalMs, readers)
		}
	}

	if showSection("events") && len(cfg.Events.Scenarios) > 0 {
		fmt.Printf("\nEvent Scenarios (%d × 3 transports):\n", len(cfg.Events.Scenarios))
		for _, s := range cfg.Events.Scenarios {
			fmt.Printf("  %s: interval=%dms\n", s.Name, s.IntervalMs)
		}
		fmt.Printf("  Transports: inotify (%s), IPC (%s), SHM (%s + %s)\n",
			cfg.Events.Inotify.WatchDir, cfg.Events.IPC.SocketPath,
			cfg.Events.SHM.SHMName, cfg.Events.SHM.FIFOPath)
	}

	if showSection("sustained") && cfg.Sustained.Enabled {
		fmt.Printf("\nSustained Test: %ds, journal=%s\n",
			cfg.Sustained.DurationSec, cfg.Sustained.JournalMode)
	}

	if showSection("reliability") && cfg.Reliability.Enabled {
		fmt.Printf("\nReliability Tests (%d):\n", len(cfg.Reliability.Tests))
		for _, t := range cfg.Reliability.Tests {
			fmt.Printf("  %s: %s (interval=%dms)\n", t.Name, t.Desc, t.IntervalMs)
		}
	}

	if showSection("stress") && cfg.Stress.Enabled {
		fmt.Printf("\nStress: cpu_loops=%d io=%v memory=%dMB\n",
			cfg.Stress.CPULoops, cfg.Stress.IOEnabled, cfg.Stress.MemoryMB)
		fmt.Println("All above scenarios re-run under system stress.")
	}

	fmt.Printf("\nThresholds:\n")
	fmt.Printf("  WAL: busy≤%.1f%% p99_write≤%dµs p99_read≤%dµs\n",
		cfg.Thresholds.WAL.MaxBusyPct,
		cfg.Thresholds.WAL.MaxP99WriteLatencyUs,
		cfg.Thresholds.WAL.MaxP99ReadLatencyUs)
	fmt.Printf("  Events: p99_dispatch≤%dµs missed≤%.1f%% coalesced≤%.1f%%\n",
		cfg.Thresholds.Events.MaxP99DispatchLatencyUs,
		cfg.Thresholds.Events.MaxMissedEventsPct,
		cfg.Thresholds.Events.MaxCoalescedPct)

	fmt.Printf("\nOutput: %s/%s → %s\n",
		cfg.Output.ResultsDir, cfg.Output.ReportFormat, cfg.Output.ReportFile)
}

func generateMinimalYAML(cfg *config.BenchConfig) string {
	return fmt.Sprintf(`# bench.yaml — Benchmark configuration
# Generated by: bench init
# Edit to match your project, then run: bench run bench.yaml

schema: %s

configs:
  directory: %s

duration_sec: %d

thresholds:
  wal:
    max_busy_pct: %.1f
    max_p99_write_latency_us: %d
  events:
    max_p99_dispatch_latency_us: %d
    max_missed_events_pct: %.1f
`,
		cfg.Schema,
		cfg.Configs.Directory,
		cfg.DurationSec,
		cfg.Thresholds.WAL.MaxBusyPct,
		cfg.Thresholds.WAL.MaxP99WriteLatencyUs,
		cfg.Thresholds.Events.MaxP99DispatchLatencyUs,
		cfg.Thresholds.Events.MaxMissedEventsPct,
	)
}
