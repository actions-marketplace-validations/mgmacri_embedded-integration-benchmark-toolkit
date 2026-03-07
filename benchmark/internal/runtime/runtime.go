// Package runtime loads the bench runtime JSON config written by the Go
// orchestrator. C binaries use bench_runtime.h; Go binaries use this package.
//
// When the file is not found, Load returns nil — callers fall back to
// their original hardcoded behavior.
package runtime

import (
	"encoding/json"
	"fmt"
	"os"
)

const DefaultPath = "/tmp/bench_runtime.json"

// Config is the top-level runtime config written by the bench CLI.
type Config struct {
	Schema     Schema      `json:"schema"`
	Subsystems []Subsystem `json:"subsystems"`
	Paths      Paths       `json:"paths"`
}

// Schema holds table metadata and auto-generated SQL.
type Schema struct {
	Table       string   `json:"table"`
	InsertSQL   string   `json:"insert_sql"`
	SelectSQL   string   `json:"select_sql"`
	ColumnCount int      `json:"column_count"`
	Columns     []Column `json:"columns"`
}

// Column is a single column's metadata for data generation.
type Column struct {
	Name     string `json:"name"`
	Affinity string `json:"affinity"`
	Hint     string `json:"hint"`
}

// Subsystem defines a config subsystem for event benchmarks.
type Subsystem struct {
	Name       string  `json:"name"`
	FieldCount int     `json:"field_count"`
	Fields     []Field `json:"fields"`
}

// Field is a single field in a subsystem.
type Field struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Min    int      `json:"min"`
	Max    int      `json:"max"`
	Values []string `json:"values"`
}

// Paths holds filesystem paths for benchmark artifacts.
type Paths struct {
	WatchDir   string `json:"watch_dir"`
	SocketPath string `json:"socket_path"`
	SHMName    string `json:"shm_name"`
	FIFOPath   string `json:"fifo_path"`
}

// Load reads the runtime config from the given path.
// Returns nil if the file doesn't exist (backwards-compatible mode).
func Load(path string) *Config {
	if path == "" {
		path = DefaultPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[runtime] No config at %s — using defaults\n", path)
		return nil
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "[runtime] Error parsing %s: %v — using defaults\n", path, err)
		return nil
	}

	fmt.Fprintf(os.Stderr, "[runtime] Loaded config: table=%s cols=%d\n",
		cfg.Schema.Table, cfg.Schema.ColumnCount)
	return &cfg
}
