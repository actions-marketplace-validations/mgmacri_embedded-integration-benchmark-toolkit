-- schema.sql — Benchmark database schema
--
-- This schema provides a realistic multi-column data logging table and a
-- configuration store table. Customize the column names and types to match
-- your application's actual schema.
--
-- IMPORTANT: NO PRAGMAs in this file.
-- Journal mode (WAL or rollback) is set by the orchestrator shell scripts
-- or programmatically by each binary. This keeps the schema neutral so the
-- same file works for both WAL and rollback-journal test scenarios.
--
-- To adapt for your project:
--   1. Replace column names with your application's fields
--   2. Add/remove columns to match your row size
--   3. Add indexes if your application uses them (they increase write cost)
--   4. Keep the second table if you have multi-process config writes

-- Primary data table: simulates high-frequency sensor/measurement logging.
-- 19 columns of mixed types (INTEGER, TEXT) to produce realistic row sizes.
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

-- Secondary config table: simulates a web service or management process
-- writing configuration key-value pairs concurrently with data logging.
CREATE TABLE IF NOT EXISTS config_store (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
