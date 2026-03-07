// Package payload generates deterministic config payloads from YAML-defined
// subsystem field schemas.
//
// This replaces the hardcoded generatePayload() functions that were previously
// copy-pasted across sentinel_writer, ipc_client, and shm_writer. Instead of
// a switch statement with hand-coded field names, the generator reads field
// definitions from the parsed bench.yaml config and produces pipe-separated
// key=value payloads with deterministic RNG-driven values.
//
// Format: key1=val1|key2=val2|...  (matching what C receivers parse)
//
// Deterministic RNG: Seed 42 (per copilot-instructions.md). Values vary per
// call via the provided *rand.Rand to simulate real config changes.
package payload

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/config"
)

// Generator produces config payloads for a set of subsystems.
type Generator struct {
	subsystems map[string]config.Subsystem
	names      []string // ordered subsystem names for round-robin
}

// NewGenerator creates a payload generator from config subsystem definitions.
func NewGenerator(subsystems map[string]config.Subsystem) *Generator {
	names := make([]string, 0, len(subsystems))
	for name := range subsystems {
		names = append(names, name)
	}
	// Sort for deterministic ordering
	sortStrings(names)

	return &Generator{
		subsystems: subsystems,
		names:      names,
	}
}

// NewGeneratorFromSpecs creates a payload generator from field specs,
// without requiring the config package. Used by binaries that load
// subsystem definitions from the runtime JSON config rather than bench.yaml.
func NewGeneratorFromSpecs(specs map[string][]ColumnSpec) *Generator {
	subsystems := make(map[string]config.Subsystem, len(specs))
	for name, cols := range specs {
		var fields []config.Field
		for _, col := range cols {
			fields = append(fields, config.Field{
				Name: col.Name,
				Type: col.Hint,
			})
		}
		subsystems[name] = config.Subsystem{
			Fields: fields,
		}
	}

	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sortStrings(names)

	return &Generator{
		subsystems: subsystems,
		names:      names,
	}
}

// SubsystemNames returns the ordered list of subsystem names.
func (g *Generator) SubsystemNames() []string {
	result := make([]string, len(g.names))
	copy(result, g.names)
	return result
}

// SubsystemCount returns the number of configured subsystems.
func (g *Generator) SubsystemCount() int {
	return len(g.names)
}

// SubsystemAtIndex returns the subsystem name at the given index (for round-robin).
func (g *Generator) SubsystemAtIndex(idx int) string {
	if len(g.names) == 0 {
		return ""
	}
	return g.names[idx%len(g.names)]
}

// Generate produces a pipe-separated key=value payload for the given subsystem.
// Values are deterministic per the provided RNG. The seqNum is available for
// fields that should vary monotonically (e.g., record counters).
//
// Returns the payload string and its byte length.
func (g *Generator) Generate(subsystem string, rng *rand.Rand, seqNum int) (string, int) {
	sub, ok := g.subsystems[subsystem]
	if !ok {
		return "", 0
	}

	var b strings.Builder
	for i, f := range sub.Fields {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(f.Name)
		b.WriteByte('=')

		switch f.Type {
		case "int":
			val := generateInt(f, rng, seqNum)
			b.WriteString(fmt.Sprintf("%d", val))
		case "text":
			val := generateText(f, rng)
			b.WriteString(val)
		}
	}

	s := b.String()
	return s, len(s)
}

// generateInt produces a deterministic integer value for a field.
func generateInt(f config.Field, rng *rand.Rand, seqNum int) int {
	lo := f.Min
	hi := f.Max
	if hi <= lo {
		hi = lo + 1
	}
	_ = seqNum // available for monotonic fields if needed
	return lo + rng.Intn(hi-lo+1)
}

// generateText picks a deterministic text value from the field's Values list.
func generateText(f config.Field, rng *rand.Rand) string {
	if len(f.Values) == 0 {
		return ""
	}
	return f.Values[rng.Intn(len(f.Values))]
}

// sortStrings sorts a string slice in-place (simple insertion sort to avoid
// importing sort package for a tiny helper).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// GenerateForC produces a config payload line in the format expected by C receivers:
//
//	subsystem:timestamp_ns:key1=val1|key2=val2|...\n
//
// This is the wire format for IPC (Unix socket) messages.
// The timestamp_ns should be CLOCK_REALTIME from time.Now().UnixNano().
func (g *Generator) GenerateForC(subsystem string, rng *rand.Rand, seqNum int, timestampNs int64) string {
	payload, _ := g.Generate(subsystem, rng, seqNum)
	return fmt.Sprintf("%s:%d:%s\n", subsystem, timestampNs, payload)
}

// GenerateRow produces deterministic column values for a SQL INSERT based on
// schema column metadata. This replaces the hardcoded bind calls in c_writer
// and go_writer.
//
// Returns a slice of interface{} values matching the column order, suitable
// for use with database/sql Exec(sql, values...).
func GenerateRow(cols []ColumnSpec, rng *rand.Rand, seqNum int) []interface{} {
	values := make([]interface{}, len(cols))
	for i, col := range cols {
		switch col.Affinity {
		case "integer":
			values[i] = generateColInt(col, rng, seqNum)
		case "text":
			values[i] = generateColText(col, rng, seqNum)
		default:
			values[i] = generateColInt(col, rng, seqNum)
		}
	}
	return values
}

// ColumnSpec defines a column for data generation purposes.
// Derived from schema.Column but with generation-specific hints.
type ColumnSpec struct {
	Name     string
	Affinity string // "integer" or "text"
	Hint     string // "date", "time", "flag", "serial", "label", "id", "" (generic)
}

// ColumnSpecsFromSchema converts schema columns to generation specs.
// Applies heuristic naming rules to assign hints for realistic data.
func ColumnSpecsFromSchema(names []string, affinities []string) []ColumnSpec {
	specs := make([]ColumnSpec, len(names))
	for i := range names {
		specs[i] = ColumnSpec{
			Name:     names[i],
			Affinity: affinities[i],
			Hint:     inferHint(names[i]),
		}
	}
	return specs
}

// inferHint guesses a generation hint from a column name.
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
	case strings.Contains(lower, "id") || lower == "record_id":
		return "id"
	default:
		return ""
	}
}

// generateColInt produces a realistic integer value based on the column hint.
func generateColInt(col ColumnSpec, rng *rand.Rand, seqNum int) interface{} {
	switch col.Hint {
	case "id":
		return seqNum + 1
	default:
		return rng.Intn(1000)
	}
}

// generateColText produces a realistic text value based on the column hint.
func generateColText(col ColumnSpec, rng *rand.Rand, seqNum int) interface{} {
	switch col.Hint {
	case "date":
		return fmt.Sprintf("2024-%02d-%02d", (seqNum%12)+1, (seqNum%28)+1)
	case "time":
		return fmt.Sprintf("%02d:%02d:%02d", rng.Intn(24), rng.Intn(60), rng.Intn(60))
	case "flag":
		if rng.Intn(100) < 80 {
			return "P"
		}
		return "F"
	case "serial":
		return fmt.Sprintf("BENCH-%03d", rng.Intn(100))
	case "label":
		labels := []string{"A", "B", "C", "S", "X"}
		return labels[rng.Intn(len(labels))]
	case "name":
		return "BenchOp"
	default:
		return fmt.Sprintf("val_%d", rng.Intn(1000))
	}
}
