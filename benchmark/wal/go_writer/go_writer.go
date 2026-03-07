// Package main implements go_writer — Go config writer for SQLite WAL benchmark.
//
// Simulates a web service or management process writing configuration data
// concurrently with the C data writer. Writes to config_store table.
//
// CLI: ./go_writer <db_path> <interval_ms> <duration_sec>
// Output: JSON to stdout, progress to stderr
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/runtime"
	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/stats"
)

type Result struct {
	Role             string             `json:"role"`
	JournalMode      string             `json:"journal_mode"`
	BusyTimeoutMs    int                `json:"busy_timeout_ms"`
	IntervalMs       int                `json:"interval_ms"`
	DurationSec      int                `json:"duration_sec"`
	TotalWrites      int                `json:"total_writes"`
	SuccessfulWrites int                `json:"successful_writes"`
	SQLiteBusyCount  int                `json:"sqlite_busy_count"`
	SQLiteErrorCount int                `json:"sqlite_error_count"`
	WriteLatencyUs   stats.LatencyStats `json:"write_latency_us"`
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "Usage: %s <db_path> <interval_ms> <duration_sec>\n", os.Args[0])
		os.Exit(1)
	}

	dbPath := os.Args[1]
	intervalMs, _ := strconv.Atoi(os.Args[2])
	durationSec, _ := strconv.Atoi(os.Args[3])

	if intervalMs <= 0 || durationSec <= 0 {
		fmt.Fprintf(os.Stderr, "[go_writer] Invalid parameters\n")
		os.Exit(1)
	}

	dsn := dbPath + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_writer] Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		journalMode = "unknown"
	}

	fmt.Fprintf(os.Stderr, "[go_writer] db=%s interval=%dms duration=%ds journal=%s\n",
		dbPath, intervalMs, durationSec, journalMode)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(durationSec)*time.Second)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// INSERT SQL — use runtime config if available, otherwise hardcoded
	rt := runtime.Load("")
	var insertSQL string
	var useRuntime bool

	if rt != nil && rt.Schema.InsertSQL != "" {
		insertSQL = rt.Schema.InsertSQL
		useRuntime = true
		fmt.Fprintf(os.Stderr, "[go_writer] Using runtime INSERT: %s\n", insertSQL)
	} else {
		insertSQL = "INSERT INTO config_store (key, value, updated_at) VALUES (?, ?, ?)"
		useRuntime = false
		fmt.Fprintf(os.Stderr, "[go_writer] Using hardcoded INSERT (no runtime config)\n")
	}
	_ = useRuntime // reserved for future dynamic data generation

	stmt, err := db.PrepareContext(ctx, insertSQL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_writer] Prepare failed: %v\n", err)
		os.Exit(1)
	}
	defer stmt.Close()

	rng := rand.New(rand.NewSource(42))
	configKeys := []string{"wifi_ssid", "motor_speed", "display_brightness", "auto_shutdown", "language"}

	var latencies []int64
	totalWrites := 0
	successfulWrites := 0
	busyCount := 0
	errorCount := 0

	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			goto done
		case <-ticker.C:
			key := configKeys[rng.Intn(len(configKeys))]
			value := fmt.Sprintf("val_%d", rng.Intn(1000))
			updatedAt := time.Now().Format("2006-01-02 15:04:05")

			start := time.Now()
			_, err := stmt.ExecContext(ctx, key, value, updatedAt)
			elapsed := time.Since(start)

			totalWrites++

			if err != nil {
				if strings.Contains(err.Error(), "database is locked") {
					busyCount++
					fmt.Fprintf(os.Stderr, "[go_writer] SQLITE_BUSY on write #%d\n", totalWrites)
				} else if ctx.Err() != nil {
					goto done
				} else {
					errorCount++
					fmt.Fprintf(os.Stderr, "[go_writer] Error on write #%d: %v\n", totalWrites, err)
				}
				continue
			}

			successfulWrites++
			latencies = append(latencies, elapsed.Microseconds())

			if totalWrites%50 == 0 {
				fmt.Fprintf(os.Stderr, "[go_writer] %d writes completed\n", totalWrites)
			}
		}
	}

done:
	st := stats.Compute(latencies)

	result := Result{
		Role:             "go_writer",
		JournalMode:      journalMode,
		BusyTimeoutMs:    5000,
		IntervalMs:       intervalMs,
		DurationSec:      durationSec,
		TotalWrites:      totalWrites,
		SuccessfulWrites: successfulWrites,
		SQLiteBusyCount:  busyCount,
		SQLiteErrorCount: errorCount,
		WriteLatencyUs:   st,
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(out))

	fmt.Fprintf(os.Stderr, "[go_writer] Done: %d/%d writes, %d busy, %d errors\n",
		successfulWrites, totalWrites, busyCount, errorCount)
}
