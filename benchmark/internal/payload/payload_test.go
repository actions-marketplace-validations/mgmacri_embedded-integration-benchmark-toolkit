package payload

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/config"
)

func testSubsystems() map[string]config.Subsystem {
	return map[string]config.Subsystem{
		"sensor_calibration": {
			Fields: []config.Field{
				{Name: "temp_offset", Type: "int", Min: -50, Max: 50},
				{Name: "pressure_gain", Type: "int", Min: 900, Max: 1100},
				{Name: "sensor_id", Type: "text", Values: []string{"SNS-001", "SNS-002", "SNS-003"}},
			},
		},
		"network_config": {
			Fields: []config.Field{
				{Name: "ip_addr", Type: "text", Values: []string{"10.0.1.100", "10.0.1.101"}},
				{Name: "mtu", Type: "int", Min: 576, Max: 9000},
			},
		},
	}
}

func TestNewGenerator(t *testing.T) {
	g := NewGenerator(testSubsystems())

	if g.SubsystemCount() != 2 {
		t.Errorf("SubsystemCount = %d, want 2", g.SubsystemCount())
	}

	names := g.SubsystemNames()
	// Should be sorted alphabetically
	if names[0] != "network_config" {
		t.Errorf("first subsystem = %q, want 'network_config'", names[0])
	}
	if names[1] != "sensor_calibration" {
		t.Errorf("second subsystem = %q, want 'sensor_calibration'", names[1])
	}
}

func TestGenerate(t *testing.T) {
	g := NewGenerator(testSubsystems())
	rng := rand.New(rand.NewSource(42))

	payload, size := g.Generate("sensor_calibration", rng, 1)

	if size == 0 {
		t.Fatal("payload size is 0")
	}
	if size != len(payload) {
		t.Errorf("size = %d, but len(payload) = %d", size, len(payload))
	}

	// Should contain all field names
	if !strings.Contains(payload, "temp_offset=") {
		t.Errorf("payload missing 'temp_offset=': %s", payload)
	}
	if !strings.Contains(payload, "pressure_gain=") {
		t.Errorf("payload missing 'pressure_gain=': %s", payload)
	}
	if !strings.Contains(payload, "sensor_id=") {
		t.Errorf("payload missing 'sensor_id=': %s", payload)
	}

	// Should be pipe-separated
	parts := strings.Split(payload, "|")
	if len(parts) != 3 {
		t.Errorf("expected 3 fields, got %d: %s", len(parts), payload)
	}
}

func TestGenerateDeterministic(t *testing.T) {
	g := NewGenerator(testSubsystems())

	// Same seed + same seqNum → same output
	rng1 := rand.New(rand.NewSource(42))
	rng2 := rand.New(rand.NewSource(42))

	p1, _ := g.Generate("sensor_calibration", rng1, 1)
	p2, _ := g.Generate("sensor_calibration", rng2, 1)

	if p1 != p2 {
		t.Errorf("payloads not deterministic:\n  1: %s\n  2: %s", p1, p2)
	}
}

func TestGenerateForC(t *testing.T) {
	g := NewGenerator(testSubsystems())
	rng := rand.New(rand.NewSource(42))

	msg := g.GenerateForC("network_config", rng, 1, 1700000000000000000)

	if !strings.HasPrefix(msg, "network_config:1700000000000000000:") {
		t.Errorf("wrong IPC format: %s", msg)
	}
	if !strings.HasSuffix(msg, "\n") {
		t.Error("IPC message should end with newline")
	}
}

func TestSubsystemAtIndex(t *testing.T) {
	g := NewGenerator(testSubsystems())

	// Round-robin across subsystems
	for i := 0; i < 10; i++ {
		name := g.SubsystemAtIndex(i)
		if name == "" {
			t.Errorf("SubsystemAtIndex(%d) returned empty", i)
		}
	}

	// Verify wrapping
	name0 := g.SubsystemAtIndex(0)
	name2 := g.SubsystemAtIndex(2)
	if name0 != name2 {
		t.Errorf("round-robin broken: idx 0=%q, idx 2=%q", name0, name2)
	}
}

func TestGenerateUnknownSubsystem(t *testing.T) {
	g := NewGenerator(testSubsystems())
	rng := rand.New(rand.NewSource(42))

	payload, size := g.Generate("nonexistent", rng, 1)
	if payload != "" || size != 0 {
		t.Errorf("unknown subsystem should return empty, got %q (size %d)", payload, size)
	}
}

func TestGenerateRow(t *testing.T) {
	specs := []ColumnSpec{
		{Name: "record_id", Affinity: "integer", Hint: "id"},
		{Name: "date", Affinity: "text", Hint: "date"},
		{Name: "time", Affinity: "text", Hint: "time"},
		{Name: "result_flag", Affinity: "text", Hint: "flag"},
		{Name: "value", Affinity: "integer", Hint: ""},
		{Name: "operator_name", Affinity: "text", Hint: "name"},
		{Name: "device_serial", Affinity: "text", Hint: "serial"},
		{Name: "source_label", Affinity: "text", Hint: "label"},
	}

	rng := rand.New(rand.NewSource(42))
	row := GenerateRow(specs, rng, 0)

	if len(row) != len(specs) {
		t.Fatalf("row len = %d, want %d", len(row), len(specs))
	}

	// record_id (hint=id) should be seqNum+1 = 1
	if row[0] != 1 {
		t.Errorf("record_id = %v, want 1", row[0])
	}

	// date should match date format
	dateStr, ok := row[1].(string)
	if !ok {
		t.Errorf("date should be string, got %T", row[1])
	}
	if !strings.Contains(dateStr, "-") {
		t.Errorf("date doesn't look like a date: %s", dateStr)
	}

	// operator_name (hint=name) should be "BenchOp"
	if row[5] != "BenchOp" {
		t.Errorf("operator_name = %v, want BenchOp", row[5])
	}
}

func TestColumnSpecsFromSchema(t *testing.T) {
	names := []string{"record_id", "date", "result_flag", "device_serial", "value"}
	affinities := []string{"integer", "text", "text", "text", "integer"}

	specs := ColumnSpecsFromSchema(names, affinities)

	if len(specs) != 5 {
		t.Fatalf("specs len = %d, want 5", len(specs))
	}

	// Check hints
	expected := []string{"id", "date", "flag", "serial", ""}
	for i, spec := range specs {
		if spec.Hint != expected[i] {
			t.Errorf("specs[%d].Hint = %q, want %q (name=%q)", i, spec.Hint, expected[i], spec.Name)
		}
	}
}

func TestSortStrings(t *testing.T) {
	s := []string{"charlie", "alpha", "bravo"}
	sortStrings(s)
	if s[0] != "alpha" || s[1] != "bravo" || s[2] != "charlie" {
		t.Errorf("sort failed: %v", s)
	}
}
