// Package schema introspects SQL CREATE TABLE statements to automatically
// generate INSERT, SELECT, and bind-parameter metadata for schema-agnostic
// benchmarking.
//
// Given a .sql file (e.g., schema.sql), this package:
//  1. Parses all CREATE TABLE statements
//  2. Extracts column names and type affinities (INTEGER, TEXT)
//  3. Generates parameterized INSERT and SELECT SQL
//  4. Produces a ColumnMeta list for deterministic data generation
//
// This eliminates the need to hardcode 19-column INSERTs in benchmark binaries.
// Instead, binaries receive the generated SQL and column metadata at runtime
// via the orchestrator's runtime JSON config.
//
// Limitations:
//   - Only parses simple CREATE TABLE ... (...) syntax
//   - Does not handle CREATE TABLE AS SELECT, virtual tables, or CTEs
//   - Type detection uses SQLite type affinity rules (INTEGER, TEXT, REAL, BLOB)
//   - Constraints (PRIMARY KEY, NOT NULL, etc.) are noted but not enforced
package schema

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Table represents a parsed CREATE TABLE definition.
type Table struct {
	Name    string       `json:"name"`
	Columns []Column     `json:"columns"`
	SQL     GeneratedSQL `json:"sql"`
}

// Column represents a single column from a CREATE TABLE statement.
type Column struct {
	Name      string `json:"name"`
	Type      string `json:"type"`        // Original type text (e.g., "INTEGER", "TEXT")
	Affinity  string `json:"affinity"`    // Normalized: "integer", "text", "real", "blob", "numeric"
	IsPK      bool   `json:"is_pk"`       // Has PRIMARY KEY constraint
	IsAutoInc bool   `json:"is_autoinc"`  // Has AUTOINCREMENT
	IsNotNull bool   `json:"is_not_null"` // Has NOT NULL constraint
}

// GeneratedSQL holds auto-generated parameterized SQL for the table.
type GeneratedSQL struct {
	Insert      string `json:"insert"`       // INSERT INTO t (cols...) VALUES (?, ?, ...)
	Select      string `json:"select"`       // SELECT cols... FROM t
	SelectWhere string `json:"select_where"` // SELECT cols... FROM t WHERE <first_int_col> = ?
	ColumnCount int    `json:"column_count"` // Number of bindable columns (excl autoincrement)
}

// Introspect parses a .sql file and returns Table definitions for all
// CREATE TABLE statements found.
func Introspect(path string) ([]Table, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	return IntrospectSQL(string(data))
}

// IntrospectSQL parses SQL text and returns Table definitions.
func IntrospectSQL(sql string) ([]Table, error) {
	// Match CREATE TABLE [IF NOT EXISTS] <name> ( ... );
	// Use (?s) for dotall mode so . matches newlines in column definitions.
	re := regexp.MustCompile(`(?si)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)\s*\((.*?)\)\s*;`)

	matches := re.FindAllStringSubmatch(sql, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no CREATE TABLE statements found")
	}

	var tables []Table
	for _, m := range matches {
		tableName := m[1]
		colDefs := m[2]

		cols, err := parseColumns(colDefs)
		if err != nil {
			return nil, fmt.Errorf("table %q: %w", tableName, err)
		}

		genSQL := generateSQL(tableName, cols)

		tables = append(tables, Table{
			Name:    tableName,
			Columns: cols,
			SQL:     genSQL,
		})
	}

	return tables, nil
}

// parseColumns splits the column definition text and extracts column metadata.
func parseColumns(defs string) ([]Column, error) {
	// Split on commas, but be careful with parenthesized constraints
	parts := splitColumnDefs(defs)

	var cols []Column
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Skip table-level constraints (PRIMARY KEY(...), UNIQUE(...), etc.)
		upper := strings.ToUpper(part)
		if strings.HasPrefix(upper, "PRIMARY KEY") ||
			strings.HasPrefix(upper, "UNIQUE") ||
			strings.HasPrefix(upper, "CHECK") ||
			strings.HasPrefix(upper, "FOREIGN KEY") ||
			strings.HasPrefix(upper, "CONSTRAINT") {
			continue
		}

		col, err := parseOneColumn(part)
		if err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}

	if len(cols) == 0 {
		return nil, fmt.Errorf("no columns found")
	}

	return cols, nil
}

// splitColumnDefs splits column definitions respecting parentheses depth.
func splitColumnDefs(defs string) []string {
	var parts []string
	var current strings.Builder
	depth := 0

	for _, ch := range defs {
		switch ch {
		case '(':
			depth++
			current.WriteRune(ch)
		case ')':
			depth--
			current.WriteRune(ch)
		case ',':
			if depth == 0 {
				parts = append(parts, current.String())
				current.Reset()
			} else {
				current.WriteRune(ch)
			}
		default:
			current.WriteRune(ch)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// parseOneColumn parses a single column definition like "name TEXT NOT NULL".
func parseOneColumn(def string) (Column, error) {
	// Tokenize: first token is name, second is type (if present), rest is constraints
	tokens := strings.Fields(def)
	if len(tokens) == 0 {
		return Column{}, fmt.Errorf("empty column definition")
	}

	col := Column{
		Name: tokens[0],
	}

	if len(tokens) > 1 {
		col.Type = tokens[1]
	} else {
		col.Type = "" // SQLite allows typeless columns
	}

	col.Affinity = typeAffinity(col.Type)

	// Check for constraints in the remaining tokens
	upper := strings.ToUpper(def)
	col.IsPK = strings.Contains(upper, "PRIMARY KEY")
	col.IsAutoInc = strings.Contains(upper, "AUTOINCREMENT")
	col.IsNotNull = strings.Contains(upper, "NOT NULL")

	return col, nil
}

// typeAffinity returns the SQLite type affinity for a given type name.
// Follows SQLite's type affinity rules:
// https://www.sqlite.org/datatype3.html#type_affinity
func typeAffinity(typeName string) string {
	upper := strings.ToUpper(typeName)

	// Rule 1: Contains "INT" → integer affinity
	if strings.Contains(upper, "INT") {
		return "integer"
	}
	// Rule 2: Contains "CHAR", "CLOB", or "TEXT" → text affinity
	if strings.Contains(upper, "CHAR") || strings.Contains(upper, "CLOB") || strings.Contains(upper, "TEXT") {
		return "text"
	}
	// Rule 3: Contains "BLOB" or empty → blob/none affinity
	if strings.Contains(upper, "BLOB") || upper == "" {
		return "blob"
	}
	// Rule 4: Contains "REAL", "FLOA", or "DOUB" → real affinity
	if strings.Contains(upper, "REAL") || strings.Contains(upper, "FLOA") || strings.Contains(upper, "DOUB") {
		return "real"
	}
	// Rule 5: Otherwise → numeric affinity
	return "numeric"
}

// generateSQL produces parameterized INSERT and SELECT statements.
func generateSQL(tableName string, cols []Column) GeneratedSQL {
	// For INSERT: skip autoincrement columns (SQLite assigns automatically)
	var insertCols []string
	var placeholders []string
	for _, c := range cols {
		if c.IsAutoInc {
			continue
		}
		insertCols = append(insertCols, c.Name)
		placeholders = append(placeholders, "?")
	}

	insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);",
		tableName,
		strings.Join(insertCols, ", "),
		strings.Join(placeholders, ", "))

	// For SELECT: include all columns
	var selectCols []string
	for _, c := range cols {
		selectCols = append(selectCols, c.Name)
	}

	selectSQL := fmt.Sprintf("SELECT %s FROM %s",
		strings.Join(selectCols, ", "),
		tableName)

	// SELECT with WHERE on first integer column (for filtered reads)
	var selectWhere string
	for _, c := range cols {
		if c.Affinity == "integer" && !c.IsPK && !c.IsAutoInc {
			selectWhere = fmt.Sprintf("%s WHERE %s = ?", selectSQL, c.Name)
			break
		}
	}
	if selectWhere == "" {
		// Fallback: no integer column found, use first column
		if len(cols) > 0 {
			selectWhere = fmt.Sprintf("%s WHERE %s = ?", selectSQL, cols[0].Name)
		}
	}

	return GeneratedSQL{
		Insert:      insertSQL,
		Select:      selectSQL,
		SelectWhere: selectWhere,
		ColumnCount: len(insertCols),
	}
}

// FindTable returns the table with the given name, or nil if not found.
func FindTable(tables []Table, name string) *Table {
	for i := range tables {
		if strings.EqualFold(tables[i].Name, name) {
			return &tables[i]
		}
	}
	return nil
}

// BindableColumns returns columns suitable for data binding (excluding AUTOINCREMENT).
func (t *Table) BindableColumns() []Column {
	var cols []Column
	for _, c := range t.Columns {
		if !c.IsAutoInc {
			cols = append(cols, c)
		}
	}
	return cols
}

// IntegerColumns returns columns with integer affinity (for WHERE clause targets).
func (t *Table) IntegerColumns() []Column {
	var cols []Column
	for _, c := range t.Columns {
		if c.Affinity == "integer" {
			cols = append(cols, c)
		}
	}
	return cols
}
