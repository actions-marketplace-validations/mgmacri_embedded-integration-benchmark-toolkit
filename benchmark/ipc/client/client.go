// Package main implements the IPC socket client for latency comparison.
//
// Connects to the C server's Unix domain socket and sends
// "subsystem:timestamp_ns:key1=val1|key2=val2|...\n" messages with realistic
// config payloads. Measures write latency for fair comparison with sentinel_writer.
//
// CLI: ./ipc_client <socket_path> <interval_ms> <duration_sec>
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
	"net"
	"os"
	"os/signal"
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
	WriteLatencyUs    stats.LatencyStats `json:"write_latency_us"`
	WritesBySubsystem map[string]int     `json:"writes_by_subsystem"`
	AvgPayloadBytes   int                `json:"avg_payload_bytes"`
}

// generatePayloadLegacy produces payloads using the original hardcoded schemas.
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

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "Usage: %s <socket_path> <interval_ms> <duration_sec>\n", os.Args[0])
		os.Exit(1)
	}

	socketPath := os.Args[1]
	intervalMs, _ := strconv.Atoi(os.Args[2])
	durationSec, _ := strconv.Atoi(os.Args[3])

	if intervalMs <= 0 || durationSec <= 0 {
		fmt.Fprintf(os.Stderr, "[ipc_client] Invalid parameters\n")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "[ipc_client] socket=%s interval=%dms duration=%ds\n",
		socketPath, intervalMs, durationSec)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(durationSec)*time.Second)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Connect to the C server
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ipc_client] Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Fprintf(os.Stderr, "[ipc_client] Connected to %s\n", socketPath)

	// --- Load runtime config (optional, for dynamic subsystems) ---
	rt := runtime.Load("")
	subsystems := []string{"sensor_calibration", "network_config", "user_profiles"}
	var gen *payload.Generator
	if rt != nil && len(rt.Subsystems) > 0 {
		subsystems = nil
		specs := make(map[string][]payload.ColumnSpec)
		for _, s := range rt.Subsystems {
			var cs []payload.ColumnSpec
			for _, f := range s.Fields {
				cs = append(cs, payload.ColumnSpec{Name: f.Name, Hint: f.Type})
			}
			specs[s.Name] = cs
			subsystems = append(subsystems, s.Name)
		}
		gen = payload.NewGeneratorFromSpecs(specs)
		fmt.Fprintf(os.Stderr, "[ipc_client] Runtime config: %d subsystems\n", len(subsystems))
	}
	rng := rand.New(rand.NewSource(42))

	var latencies []int64
	subsystemCounts := make(map[string]int)
	totalWrites := 0
	totalPayloadBytes := 0

	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			goto done
		case <-ticker.C:
			sub := subsystems[rng.Intn(len(subsystems))]
			var pld string
			if gen != nil {
				pld, _ = gen.Generate(sub, rng, totalWrites)
			} else {
				pld = generatePayloadLegacy(sub, rng, totalWrites)
			}

			// Record CLOCK_REALTIME timestamp and send with payload
			timestamp := time.Now().UnixNano()
			msg := fmt.Sprintf("%s:%d:%s\n", sub, timestamp, pld)

			start := time.Now()
			_, err := conn.Write([]byte(msg))
			elapsed := time.Since(start)

			totalWrites++

			if err != nil {
				fmt.Fprintf(os.Stderr, "[ipc_client] Write error #%d: %v\n", totalWrites, err)
				continue
			}

			latencies = append(latencies, elapsed.Microseconds())
			subsystemCounts[sub]++
			totalPayloadBytes += len(pld)

			if totalWrites%100 == 0 {
				fmt.Fprintf(os.Stderr, "[ipc_client] %d writes completed\n", totalWrites)
			}
		}
	}

done:
	latStats := stats.Compute(latencies)

	avgPayload := 0
	if totalWrites > 0 {
		avgPayload = totalPayloadBytes / totalWrites
	}

	result := Result{
		Role:              "ipc_socket_client",
		TotalWrites:       totalWrites,
		DurationSec:       durationSec,
		IntervalMs:        intervalMs,
		WriteLatencyUs:    latStats,
		WritesBySubsystem: subsystemCounts,
		AvgPayloadBytes:   avgPayload,
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(out))

	fmt.Fprintf(os.Stderr, "[ipc_client] Done: %d writes, avg payload=%d bytes\n", totalWrites, avgPayload)
}
