package schema

import (
	"strings"
	"testing"
)

const testSchema = `
-- Test schema matching the project's schema.sql
CREATE TABLE IF NOT EXISTS sample_data (
    record_id        INTEGER,
    date             TEXT,
    time             TEXT,
    target_value_1   INTEGER,
    target_value_2   INTEGER,
    result_flag      TEXT,
    actual_value_1   INTEGER,
    actual_value_2   INTEGER,
    final_value_1    INTEGER,
    unit_type        INTEGER,
    duration_ms      INTEGER,
    operator_name    TEXT,
    device_serial    TEXT,
    coord_x          INTEGER,
    coord_y          INTEGER,
    source_label     TEXT,
    category_id      INTEGER,
    final_value_2    INTEGER,
    reserved         INTEGER
);

CREATE TABLE IF NOT EXISTS config_store (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
`

func TestIntrospectSQL(t *testing.T) {
	tables, err := IntrospectSQL(testSchema)
	if err != nil {
		t.Fatalf("IntrospectSQL() error: %v", err)
	}

	if len(tables) != 2 {
		t.Fatalf("tables count = %d, want 2", len(tables))
	}

	// Verify sample_data table
	sd := FindTable(tables, "sample_data")
	if sd == nil {
		t.Fatal("sample_data table not found")
	}
	if len(sd.Columns) != 19 {
		t.Errorf("sample_data columns = %d, want 19", len(sd.Columns))
	}

	// Check column types
	recordID := sd.Columns[0]
	if recordID.Name != "record_id" {
		t.Errorf("first column = %q, want 'record_id'", recordID.Name)
	}
	if recordID.Affinity != "integer" {
		t.Errorf("record_id affinity = %q, want 'integer'", recordID.Affinity)
	}

	date := sd.Columns[1]
	if date.Name != "date" {
		t.Errorf("second column = %q, want 'date'", date.Name)
	}
	if date.Affinity != "text" {
		t.Errorf("date affinity = %q, want 'text'", date.Affinity)
	}

	// Verify generated INSERT has 19 columns (no autoincrement to skip)
	if sd.SQL.ColumnCount != 19 {
		t.Errorf("sample_data bindable columns = %d, want 19", sd.SQL.ColumnCount)
	}
	if !strings.Contains(sd.SQL.Insert, "INSERT INTO sample_data") {
		t.Errorf("INSERT SQL missing table name: %s", sd.SQL.Insert)
	}
	if !strings.HasPrefix(sd.SQL.Select, "SELECT record_id") {
		t.Errorf("SELECT SQL unexpected: %s", sd.SQL.Select)
	}

	// Verify config_store table
	cs := FindTable(tables, "config_store")
	if cs == nil {
		t.Fatal("config_store table not found")
	}
	if len(cs.Columns) != 4 {
		t.Errorf("config_store columns = %d, want 4", len(cs.Columns))
	}

	// id should be PK + AUTOINCREMENT
	id := cs.Columns[0]
	if !id.IsPK {
		t.Error("config_store.id should be PRIMARY KEY")
	}
	if !id.IsAutoInc {
		t.Error("config_store.id should be AUTOINCREMENT")
	}

	// Generated INSERT should skip autoincrement column
	if cs.SQL.ColumnCount != 3 {
		t.Errorf("config_store bindable columns = %d, want 3", cs.SQL.ColumnCount)
	}
	if strings.Contains(cs.SQL.Insert, "id,") || strings.HasPrefix(cs.SQL.Insert, "INSERT INTO config_store (id") {
		t.Errorf("INSERT should skip autoincrement 'id' column: %s", cs.SQL.Insert)
	}
}

func TestIntrospectRealSchema(t *testing.T) {
	// Test the actual schema.sql file from the project root
	tables, err := Introspect("../../../schema.sql")
	if err != nil {
		t.Skipf("schema.sql not found (expected when running outside project root): %v", err)
	}

	if len(tables) != 2 {
		t.Errorf("tables = %d, want 2", len(tables))
	}

	sd := FindTable(tables, "sample_data")
	if sd == nil {
		t.Fatal("sample_data not found in schema.sql")
	}
	if len(sd.Columns) != 19 {
		t.Errorf("sample_data columns = %d, want 19", len(sd.Columns))
	}
}

func TestTypeAffinity(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"INTEGER", "integer"},
		{"INT", "integer"},
		{"TINYINT", "integer"},
		{"BIGINT", "integer"},
		{"TEXT", "text"},
		{"VARCHAR(255)", "text"},
		{"CHAR(10)", "text"},
		{"CLOB", "text"},
		{"BLOB", "blob"},
		{"", "blob"},
		{"REAL", "real"},
		{"DOUBLE", "real"},
		{"FLOAT", "real"},
		{"NUMERIC", "numeric"},
		{"DECIMAL(10,5)", "numeric"},
		{"BOOLEAN", "numeric"},
	}

	for _, tt := range tests {
		got := typeAffinity(tt.input)
		if got != tt.want {
			t.Errorf("typeAffinity(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFindTable(t *testing.T) {
	tables := []Table{
		{Name: "sample_data"},
		{Name: "config_store"},
	}

	if FindTable(tables, "sample_data") == nil {
		t.Error("FindTable(sample_data) returned nil")
	}
	if FindTable(tables, "SAMPLE_DATA") == nil {
		t.Error("FindTable(SAMPLE_DATA) case-insensitive should work")
	}
	if FindTable(tables, "nonexistent") != nil {
		t.Error("FindTable(nonexistent) should return nil")
	}
}

func TestSelectWhereFallback(t *testing.T) {
	// Table with no integer columns (only TEXT)
	sql := `CREATE TABLE text_only (name TEXT, value TEXT);`
	tables, err := IntrospectSQL(sql)
	if err != nil {
		t.Fatal(err)
	}

	if len(tables) != 1 {
		t.Fatal("expected 1 table")
	}

	// Should fallback to first column for WHERE
	if !strings.Contains(tables[0].SQL.SelectWhere, "WHERE name = ?") {
		t.Errorf("SelectWhere should use first column as fallback: %s", tables[0].SQL.SelectWhere)
	}
}

func TestBindableColumns(t *testing.T) {
	tables, err := IntrospectSQL(testSchema)
	if err != nil {
		t.Fatal(err)
	}

	cs := FindTable(tables, "config_store")
	if cs == nil {
		t.Fatal("config_store not found")
	}

	bindable := cs.BindableColumns()
	if len(bindable) != 3 {
		t.Errorf("bindable columns = %d, want 3", len(bindable))
	}

	// id (autoincrement) should be excluded
	for _, c := range bindable {
		if c.Name == "id" {
			t.Error("'id' (autoincrement) should be excluded from bindable columns")
		}
	}
}
