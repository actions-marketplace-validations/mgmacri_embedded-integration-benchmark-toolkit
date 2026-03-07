package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMinimalConfig(t *testing.T) {
	// Create temp dir with minimal config
	dir := t.TempDir()

	// Create a minimal schema.sql
	schemaPath := filepath.Join(dir, "schema.sql")
	if err := os.WriteFile(schemaPath, []byte("CREATE TABLE test (id INTEGER, name TEXT);"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a config directory with one subsystem file
	configDir := filepath.Join(dir, "configs")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "test_subsystem"), []byte("key1=val1|key2=val2"), 0644); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
schema: ` + schemaPath + `
configs:
  directory: ` + configDir + `
  subsystems:
    test_subsystem:
      fields:
        - { name: key1, type: text, values: ["val1", "val2"] }
        - { name: key2, type: int, min: 0, max: 100 }
duration_sec: 10
thresholds:
  wal:
    max_busy_pct: 1.0
    max_p99_write_latency_us: 50000
  events:
    max_p99_dispatch_latency_us: 5000
    max_missed_events_pct: 0.0
`
	cfgPath := filepath.Join(dir, "bench.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Schema != schemaPath {
		t.Errorf("Schema = %q, want %q", cfg.Schema, schemaPath)
	}
	if cfg.DurationSec != 10 {
		t.Errorf("DurationSec = %d, want 10", cfg.DurationSec)
	}
	if cfg.Configs.Directory != configDir {
		t.Errorf("Configs.Directory = %q, want %q", cfg.Configs.Directory, configDir)
	}
	if len(cfg.Configs.Subsystems) != 1 {
		t.Errorf("Subsystems count = %d, want 1", len(cfg.Configs.Subsystems))
	}
	sub, ok := cfg.Configs.Subsystems["test_subsystem"]
	if !ok {
		t.Fatal("missing subsystem 'test_subsystem'")
	}
	if len(sub.Fields) != 2 {
		t.Errorf("test_subsystem fields = %d, want 2", len(sub.Fields))
	}
}

func TestLoadDefaultConfig(t *testing.T) {
	cfg := Default()

	if cfg.Schema != "schema.sql" {
		t.Errorf("Schema = %q, want %q", cfg.Schema, "schema.sql")
	}
	if cfg.DurationSec != 60 {
		t.Errorf("DurationSec = %d, want 60", cfg.DurationSec)
	}
	if len(cfg.Configs.Subsystems) != 3 {
		t.Errorf("Subsystems count = %d, want 3", len(cfg.Configs.Subsystems))
	}
	if len(cfg.WAL.Scenarios) != 6 {
		t.Errorf("WAL scenarios = %d, want 6", len(cfg.WAL.Scenarios))
	}
	if len(cfg.Events.Scenarios) != 3 {
		t.Errorf("Event scenarios = %d, want 3", len(cfg.Events.Scenarios))
	}
	if len(cfg.Reliability.Tests) != 5 {
		t.Errorf("Reliability tests = %d, want 5", len(cfg.Reliability.Tests))
	}
	if cfg.Thresholds.WAL.MaxP99WriteLatencyUs != 50000 {
		t.Errorf("WAL max p99 write = %d, want 50000", cfg.Thresholds.WAL.MaxP99WriteLatencyUs)
	}
}

func TestValidationMissingSchema(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `
configs:
  subsystems:
    test:
      fields:
        - { name: x, type: int, min: 0, max: 1 }
`
	cfgPath := filepath.Join(dir, "bench.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for missing schema, got nil")
	}
}

func TestValidationInvalidFieldType(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.sql")
	if err := os.WriteFile(schemaPath, []byte("CREATE TABLE t (id INTEGER);"), 0644); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
schema: ` + schemaPath + `
configs:
  subsystems:
    bad_sub:
      fields:
        - { name: x, type: float, min: 0, max: 1 }
`
	cfgPath := filepath.Join(dir, "bench.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid field type 'float', got nil")
	}
}

func TestApplyDefaults(t *testing.T) {
	cfg := &BenchConfig{
		Schema: "test.sql",
		Configs: ConfigsConfig{
			Subsystems: map[string]Subsystem{
				"test": {Fields: []Field{{Name: "x", Type: "int", Min: 0, Max: 1}}},
			},
		},
	}
	applyDefaults(cfg)

	if cfg.DurationSec != 60 {
		t.Errorf("DurationSec = %d, want 60", cfg.DurationSec)
	}
	if cfg.WAL.DBDir != "/tmp/wal_benchmark" {
		t.Errorf("WAL.DBDir = %q, want /tmp/wal_benchmark", cfg.WAL.DBDir)
	}
	if cfg.Output.ResultsDir != "results" {
		t.Errorf("Output.ResultsDir = %q, want results", cfg.Output.ResultsDir)
	}
	if cfg.Output.ReportFormat != "markdown" {
		t.Errorf("Output.ReportFormat = %q, want markdown", cfg.Output.ReportFormat)
	}
}
