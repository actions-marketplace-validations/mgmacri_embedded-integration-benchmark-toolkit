// Package main implements go_reader — Go data reader for SQLite WAL benchmark.
//
// Simulates a web service or API process reading data concurrently with a C writer.
// Uses database/sql + go-sqlite3 with WAL mode and busy_timeout via DSN.
//
// CLI: ./go_reader <db_path> <num_readers> <interval_ms> <duration_sec>
// Output: JSON to stdout, progress to stderr
//
// Customize:
//   - Change the SELECT query to match your schema
//   - Adjust num_readers to match your concurrent read load
//   - Change interval_ms to match your polling frequency
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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/runtime"
	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/stats"
	_ "github.com/mattn/go-sqlite3"
)

type Result struct {
	Role              string             `json:"role"`
	JournalMode       string             `json:"journal_mode"`
	BusyTimeoutMs     int                `json:"busy_timeout_ms"`
	NumReaders        int                `json:"num_readers"`
	IntervalMs        int                `json:"interval_ms"`
	DurationSec       int                `json:"duration_sec"`
	TotalReads        int64              `json:"total_reads"`
	SuccessfulReads   int64              `json:"successful_reads"`
	SQLiteBusyCount   int64              `json:"sqlite_busy_count"`
	SQLiteErrorCount  int64              `json:"sqlite_error_count"`
	RowsReturnedTotal int64              `json:"rows_returned_total"`
	RowsReturnedAvg   int64              `json:"rows_returned_avg"`
	ReadLatencyUs     stats.LatencyStats `json:"read_latency_us"`
}

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintf(os.Stderr, "Usage: %s <db_path> <num_readers> <interval_ms> <duration_sec>\n", os.Args[0])
		os.Exit(1)
	}

	dbPath := os.Args[1]
	numReaders, _ := strconv.Atoi(os.Args[2])
	intervalMs, _ := strconv.Atoi(os.Args[3])
	durationSec, _ := strconv.Atoi(os.Args[4])

	if numReaders <= 0 || intervalMs <= 0 || durationSec <= 0 {
		fmt.Fprintf(os.Stderr, "[go_reader] Invalid parameters\n")
		os.Exit(1)
	}

	// Open DB with WAL + busy_timeout via DSN
	dsn := dbPath + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_reader] Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		journalMode = "unknown"
	}

	fmt.Fprintf(os.Stderr, "[go_reader] db=%s readers=%d interval=%dms duration=%ds journal=%s\n",
		dbPath, numReaders, intervalMs, durationSec, journalMode)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(durationSec)*time.Second)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	var totalReads atomic.Int64
	var successfulReads atomic.Int64
	var busyCount atomic.Int64
	var errorCount atomic.Int64
	var rowsTotal atomic.Int64

	var mu sync.Mutex
	var allLatencies []int64

	// Read query — use runtime config if available, otherwise hardcoded
	rt := runtime.Load("")
	var query string
	var useDynamicScan bool

	if rt != nil && rt.Schema.SelectSQL != "" {
		query = rt.Schema.SelectSQL
		useDynamicScan = true
		fmt.Fprintf(os.Stderr, "[go_reader] Using runtime SELECT: %s\n", query)
	} else {
		// Hardcoded fallback — matches original schema.sql sample_data table
		query = "SELECT record_id, category_id, result_flag, final_value_1, final_value_2, unit_type FROM sample_data WHERE category_id = ?"
		useDynamicScan = false
		fmt.Fprintf(os.Stderr, "[go_reader] Using hardcoded SELECT (no runtime config)\n")
	}

	var wg sync.WaitGroup

	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(42 + int64(readerID)))
			ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					categoryID := rng.Intn(5) + 1

					start := time.Now()
					rows, err := db.QueryContext(ctx, query, categoryID)
					elapsed := time.Since(start)

					totalReads.Add(1)

					if err != nil {
						if strings.Contains(err.Error(), "database is locked") {
							busyCount.Add(1)
							fmt.Fprintf(os.Stderr, "[go_reader:%d] SQLITE_BUSY\n", readerID)
						} else if ctx.Err() != nil {
							return
						} else {
							errorCount.Add(1)
							fmt.Fprintf(os.Stderr, "[go_reader:%d] Error: %v\n", readerID, err)
						}
						continue
					}

					var count int64
					if useDynamicScan {
						// Dynamic: determine column count from result set and scan into interface{}
						cols, _ := rows.Columns()
						scanArgs := make([]interface{}, len(cols))
						scanDest := make([]interface{}, len(cols))
						for i := range scanDest {
							scanArgs[i] = &scanDest[i]
						}
						for rows.Next() {
							if err := rows.Scan(scanArgs...); err != nil {
								break
							}
							count++
						}
					} else {
						// Hardcoded: scan known columns
						for rows.Next() {
							var recordID, catID, finalVal1, finalVal2, unitType int
							var resultFlag string
							if err := rows.Scan(&recordID, &catID, &resultFlag, &finalVal1, &finalVal2, &unitType); err != nil {
								break
							}
							count++
						}
					}
					rows.Close()

					successfulReads.Add(1)
					rowsTotal.Add(count)

					mu.Lock()
					allLatencies = append(allLatencies, elapsed.Microseconds())
					mu.Unlock()

					if r := totalReads.Load(); r%100 == 0 {
						fmt.Fprintf(os.Stderr, "[go_reader] %d reads completed\n", r)
					}
				}
			}
		}(i)
	}

	wg.Wait()

	st := stats.Compute(allLatencies)

	tr := totalReads.Load()
	sr := successfulReads.Load()
	rt2 := rowsTotal.Load()
	var rowsAvg int64
	if sr > 0 {
		rowsAvg = rt2 / sr
	}

	result := Result{
		Role:              "go_reader",
		JournalMode:       journalMode,
		BusyTimeoutMs:     5000,
		NumReaders:        numReaders,
		IntervalMs:        intervalMs,
		DurationSec:       durationSec,
		TotalReads:        tr,
		SuccessfulReads:   sr,
		SQLiteBusyCount:   busyCount.Load(),
		SQLiteErrorCount:  errorCount.Load(),
		RowsReturnedTotal: rt2,
		RowsReturnedAvg:   rowsAvg,
		ReadLatencyUs:     st,
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(out))

	fmt.Fprintf(os.Stderr, "[go_reader] Done: %d/%d reads, %d busy, %d errors\n",
		sr, tr, busyCount.Load(), errorCount.Load())
}
