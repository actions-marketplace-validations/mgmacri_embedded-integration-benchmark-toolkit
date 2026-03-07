// Package main implements sentinel file writer for the inotify benchmark.
//
// Writes CLOCK_REALTIME timestamp + realistic config payload to a temp file,
// then atomically renames it to trigger the C inotify watcher.
//
// CLI: ./sentinel_writer <watch_dir> <interval_ms> <duration_sec> [flags]
// Flags:
//
//	--no-sync        Skip fsync before rename (tests write/sync race condition).
//	--burst-pairs N  Write N rapid overwrites per tick to the same filename,
//	                 testing inotify event coalescing. Default: 1.
//
// Output: JSON to stdout, progress to stderr
//
// Customize:
//   - Change subsystem names to match your config domains
//   - Adjust generatePayload() to match your config data size and format
//   - Payload format must match what the C server expects to parse
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/payload"
	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/runtime"
	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/stats"
)

type Result struct {
	Role              string             `json:"role"`
	TotalWrites       int                `json:"total_writes"`
	DurationSec       int                `json:"duration_sec"`
	IntervalMs        int                `json:"interval_ms"`
	NoSync            bool               `json:"no_sync"`
	BurstPairs        int                `json:"burst_pairs"`
	WriteLatencyUs    stats.LatencyStats `json:"write_latency_us"`
	WritesBySubsystem map[string]int     `json:"writes_by_subsystem"`
	AvgPayloadBytes   int                `json:"avg_payload_bytes"`
}

// generatePayloadLegacy produces a realistic config payload using the original
// hardcoded field schemas. Used when no runtime config is available.
func generatePayloadLegacy(subsystem string, rng *rand.Rand, seqNum int) string {
	switch subsystem {
	case "sensor_calibration":
		return fmt.Sprintf(
			"date=2024-03-%02d|direction=%d|angle_mode=%d|two_hand_mode=%d|"+
				"preset_index=%d|tool_mode=%d|data_logging=%d|live_logging=%d|"+
				"calibrated=1|maint_counter0=%d|maint_counter1=%d|"+
				"maint_counter2=%d|maint_counter3=%d|mcu_version=1234|"+
				"ui_version=5678|app_ver=2|os_ver=3|"+
				"user_level=%d|name_id=%d|record_id=%d",
			(seqNum%28)+1, rng.Intn(2), rng.Intn(2), rng.Intn(2),
			rng.Intn(5), rng.Intn(3), rng.Intn(2), rng.Intn(2),
			rng.Intn(500), rng.Intn(500), rng.Intn(100), rng.Intn(100),
			rng.Intn(5), (seqNum%5)+1, seqNum+1,
		)
	case "network_config":
		return fmt.Sprintf(
			"ssid=Device_Network_%d|passkey=Key%04d|channel=%d|"+
				"security=%d|ip_mode=%d|static_ip=192168%06d|"+
				"subnet_mask=255255255000|gateway=192168001001|"+
				"dns_primary=008008008008|dns_secondary=008008004004",
			rng.Intn(10), rng.Intn(10000), rng.Intn(11)+1,
			rng.Intn(3), rng.Intn(2), rng.Intn(999999),
		)
	case "user_profiles":
		return fmt.Sprintf(
			"user_id=%d|pin_code=%04d|access_level=%d|name=Operator_%02d|"+
				"maint_access=%d|config_access=%d|data_export=%d|"+
				"calibration_access=%d|admin_access=%d",
			rng.Intn(5)+1, rng.Intn(10000), rng.Intn(5), rng.Intn(20)+1,
			rng.Intn(2), rng.Intn(2), rng.Intn(2), rng.Intn(2), rng.Intn(2),
		)
	}
	return ""
}

func writeSentinel(watchDir, subsystem, pld string, seqNum int, noSync bool) error {
	tmpPath := filepath.Join(watchDir, "."+subsystem+".tmp")
	finalPath := filepath.Join(watchDir, subsystem)

	// Record CLOCK_REALTIME timestamp (for cross-process latency measurement)
	timestamp := time.Now().UnixNano()
	// File format: timestamp\nsequence\npayload
	// The watcher parses the sequence number (second line) for coalescing detection
	content := []byte(strconv.FormatInt(timestamp, 10) + "\n" +
		strconv.Itoa(seqNum) + "\n" + pld)

	if noSync {
		// Write without fsync — tests race condition where rename completes
		// before data is flushed to storage
		if err := os.WriteFile(tmpPath, content, 0644); err != nil {
			return err
		}
	} else {
		// Write with explicit fsync before rename (safe default)
		f, err := os.Create(tmpPath)
		if err != nil {
			return err
		}
		if _, err := f.Write(content); err != nil {
			f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}

	// Atomic rename triggers IN_MOVED_TO in the watcher
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return err
	}

	return nil
}

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Usage: %s <watch_dir> <interval_ms> <duration_sec> [--no-sync] [--burst-pairs N]\n", os.Args[0])
		os.Exit(1)
	}

	watchDir := os.Args[1]
	intervalMs, _ := strconv.Atoi(os.Args[2])
	durationSec, _ := strconv.Atoi(os.Args[3])

	// Parse optional flags
	noSync := false
	burstPairs := 1
	for i := 4; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--no-sync":
			noSync = true
		case "--burst-pairs":
			if i+1 < len(os.Args) {
				i++
				burstPairs, _ = strconv.Atoi(os.Args[i])
				if burstPairs < 1 {
					burstPairs = 1
				}
			}
		}
	}

	if intervalMs <= 0 || durationSec <= 0 {
		fmt.Fprintf(os.Stderr, "[sentinel_writer] Invalid parameters\n")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "[sentinel_writer] dir=%s interval=%dms duration=%ds no_sync=%v burst_pairs=%d\n",
		watchDir, intervalMs, durationSec, noSync, burstPairs)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(durationSec)*time.Second)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Subsystem names — use runtime config if available, otherwise hardcoded
	rt := runtime.Load("")
	var gen *payload.Generator
	var subsystems []string

	if rt != nil && len(rt.Subsystems) > 0 {
		// Build subsystem map from runtime config
		subsMap := make(map[string][]payload.ColumnSpec)
		for _, s := range rt.Subsystems {
			var fields []payload.ColumnSpec
			for _, f := range s.Fields {
				fields = append(fields, payload.ColumnSpec{
					Name: f.Name,
					Hint: f.Type,
				})
			}
			subsMap[s.Name] = fields
		}
		gen = payload.NewGeneratorFromSpecs(subsMap)
		for _, s := range rt.Subsystems {
			subsystems = append(subsystems, s.Name)
		}
		fmt.Fprintf(os.Stderr, "[sentinel_writer] Using runtime config: %d subsystems\n", len(subsystems))
	} else {
		// Hardcoded fallback
		subsystems = []string{"sensor_calibration", "network_config", "user_profiles"}
		gen = nil // will use legacy generatePayloadLegacy
		fmt.Fprintf(os.Stderr, "[sentinel_writer] Using hardcoded subsystems (no runtime config)\n")
	}

	rng := rand.New(rand.NewSource(42))

	var latencies []int64
	subsystemCounts := make(map[string]int)
	totalWrites := 0
	totalPayloadBytes := 0
	globalSeq := 0

	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			goto done
		case <-ticker.C:
			// Write burstPairs rapid overwrites to the same file per tick
			for bp := 0; bp < burstPairs; bp++ {
				sub := subsystems[rng.Intn(len(subsystems))]
				var pld string
				if gen != nil {
					pld, _ = gen.Generate(sub, rng, totalWrites)
				} else {
					pld = generatePayloadLegacy(sub, rng, totalWrites)
				}
				globalSeq++

				start := time.Now()
				err := writeSentinel(watchDir, sub, pld, globalSeq, noSync)
				elapsed := time.Since(start)

				totalWrites++

				if err != nil {
					fmt.Fprintf(os.Stderr, "[sentinel_writer] write error #%d: %v\n", totalWrites, err)
					continue
				}

				latencies = append(latencies, elapsed.Microseconds())
				subsystemCounts[sub]++
				totalPayloadBytes += len(pld)
			}

			if totalWrites%100 == 0 {
				fmt.Fprintf(os.Stderr, "[sentinel_writer] %d writes completed\n", totalWrites)
			}
		}
	}

done:
	st := stats.Compute(latencies)

	avgPayload := 0
	if totalWrites > 0 {
		avgPayload = totalPayloadBytes / totalWrites
	}

	result := Result{
		Role:              "sentinel_writer",
		TotalWrites:       totalWrites,
		DurationSec:       durationSec,
		IntervalMs:        intervalMs,
		NoSync:            noSync,
		BurstPairs:        burstPairs,
		WriteLatencyUs:    st,
		WritesBySubsystem: subsystemCounts,
		AvgPayloadBytes:   avgPayload,
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(out))

	fmt.Fprintf(os.Stderr, "[sentinel_writer] Done: %d writes, avg payload=%d bytes\n", totalWrites, avgPayload)
}
