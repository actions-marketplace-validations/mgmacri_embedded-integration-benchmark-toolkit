// Package main implements the shared memory writer for the mmap + FIFO benchmark.
//
// Opens the POSIX shared memory segment created by the C reader, writes config
// payloads with CLOCK_REALTIME timestamps, and signals via a named FIFO. Uses the
// same config payloads as the inotify writer and IPC client for fair comparison.
//
// CLI: ./shm_writer <interval_ms> <duration_sec>
// Output: JSON to stdout, progress to stderr
//
// Setup:
//
//	The C reader must be running first. It creates /dev/shm/bench_shm and a
//	FIFO at /tmp/bench_shm_fifo. This writer opens the FIFO for writing.
//
// Memory ordering:
//
//	All fields are written first, then sync/atomic.StoreUint32(&ready, seq)
//	provides release semantics (Go's sync/atomic is sequentially consistent on
//	ARM, emitting DMB barriers). Then write(fifo, 1) wakes the blocking reader.
//
// Build: CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7

//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/payload"
	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/runtime"
	"github.com/mgmacri/ipc-you-might-not-need/benchmark/internal/stats"
)

/* ---------- Shared memory layout constants (must match shm_common.h) ---------- */

const (
	shmPath       = "/dev/shm/bench_shm"
	shmSize       = 4096
	shmMaxPayload = 512
	shmFifoPath   = "/tmp/bench_shm_fifo"

	// Byte offsets in shm_message_t (verified by _Static_assert in shm_common.h)
	offsetReady         = 0
	offsetSequence      = 4
	offsetTimestampNs   = 8
	offsetPayloadLength = 16
	offsetSubsystemID   = 20
	offsetPayload       = 24

	// Subsystem IDs (must match shm_common.h)
	subsysSensorCalibration = 0
	subsysNetworkConfig     = 1
	subsysUserProfiles      = 2
)

/* ---------- Statistics ---------- */

type Result struct {
	Role              string             `json:"role"`
	TotalWrites       int                `json:"total_writes"`
	BufferBusyCount   int                `json:"buffer_busy_count"`
	DurationSec       int                `json:"duration_sec"`
	IntervalMs        int                `json:"interval_ms"`
	WriteLatencyUs    stats.LatencyStats `json:"write_latency_us"`
	WritesBySubsystem map[string]int     `json:"writes_by_subsystem"`
	AvgPayloadBytes   int                `json:"avg_payload_bytes"`
}

/* ---------- Payload generation (legacy fallback) ---------- */

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

/* ---------- Shared memory helpers ---------- */

// shmWriteMessage writes a config message to the mmap'd shared memory region.
// Returns false if the buffer is still busy (reader hasn't consumed previous message).
func shmWriteMessage(data []byte, seq uint32, subsystemID uint32, payload string, timestampNs int64) bool {
	// Check if reader has consumed the previous message
	readyPtr := (*uint32)(unsafe.Pointer(&data[offsetReady]))
	if atomic.LoadUint32(readyPtr) != 0 {
		return false // buffer busy
	}

	// Write all fields before setting ready flag
	seqPtr := (*uint32)(unsafe.Pointer(&data[offsetSequence]))
	*seqPtr = seq

	tsPtr := (*int64)(unsafe.Pointer(&data[offsetTimestampNs]))
	*tsPtr = timestampNs

	payloadBytes := []byte(payload)
	if len(payloadBytes) > shmMaxPayload-1 {
		payloadBytes = payloadBytes[:shmMaxPayload-1]
	}

	lenPtr := (*uint32)(unsafe.Pointer(&data[offsetPayloadLength]))
	*lenPtr = uint32(len(payloadBytes))

	subPtr := (*uint32)(unsafe.Pointer(&data[offsetSubsystemID]))
	*subPtr = subsystemID

	copy(data[offsetPayload:offsetPayload+len(payloadBytes)], payloadBytes)
	data[offsetPayload+len(payloadBytes)] = 0 // null-terminate

	// Release store — makes all prior writes visible to the reader's acquire load.
	// Go's sync/atomic.StoreUint32 provides sequential consistency (DMB on ARM).
	atomic.StoreUint32(readyPtr, seq)

	return true
}

func subsystemNameToID(name string) uint32 {
	switch name {
	case "sensor_calibration":
		return subsysSensorCalibration
	case "network_config":
		return subsysNetworkConfig
	case "user_profiles":
		return subsysUserProfiles
	}
	return 0
}

/* ---------- Main ---------- */

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <interval_ms> <duration_sec>\n", os.Args[0])
		os.Exit(1)
	}

	intervalMs, err := strconv.Atoi(os.Args[1])
	if err != nil || intervalMs <= 0 {
		fmt.Fprintf(os.Stderr, "[shm_writer] Invalid interval_ms: %s\n", os.Args[1])
		os.Exit(1)
	}
	durationSec, err := strconv.Atoi(os.Args[2])
	if err != nil || durationSec <= 0 {
		fmt.Fprintf(os.Stderr, "[shm_writer] Invalid duration_sec: %s\n", os.Args[2])
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "[shm_writer] interval=%dms duration=%ds\n", intervalMs, durationSec)

	/* Open shared memory segment (already created by C reader) */
	shmFile, err := os.OpenFile(shmPath, os.O_RDWR, 0666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[shm_writer] open %s: %v\n", shmPath, err)
		os.Exit(1)
	}
	defer shmFile.Close()

	shmData, err := syscall.Mmap(int(shmFile.Fd()), 0, shmSize,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[shm_writer] mmap failed: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := syscall.Munmap(shmData); err != nil {
			fmt.Fprintf(os.Stderr, "[shm_writer] munmap failed: %v\n", err)
		}
	}()

	/* Open FIFO for writing (reader must have created it) */
	fifoFile, err := os.OpenFile(shmFifoPath, os.O_WRONLY, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[shm_writer] open fifo %s: %v\n", shmFifoPath, err)
		os.Exit(1)
	}
	defer fifoFile.Close()

	fmt.Fprintf(os.Stderr, "[shm_writer] Connected: shm=%s fifo=%s\n",
		shmPath, shmFifoPath)

	/* Setup signal handling and context */
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(durationSec)*time.Second)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	/* Write loop */
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
		fmt.Fprintf(os.Stderr, "[shm_writer] Runtime config: %d subsystems\n", len(subsystems))
	}
	rng := rand.New(rand.NewSource(42))

	var latencies []int64
	subsystemCounts := make(map[string]int)
	totalWrites := 0
	totalPayloadBytes := 0
	bufferBusyCount := 0
	var seq uint32

	// FIFO signal byte
	sigByte := []byte{1}

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
			subID := subsystemNameToID(sub)
			seq++

			// Record CLOCK_REALTIME timestamp (for cross-process latency measurement)
			timestampNs := time.Now().UnixNano()

			start := time.Now()
			ok := shmWriteMessage(shmData, seq, subID, pld, timestampNs)
			if !ok {
				bufferBusyCount++
				totalWrites++
				if totalWrites%100 == 0 {
					fmt.Fprintf(os.Stderr, "[shm_writer] %d writes (%d busy)\n", totalWrites, bufferBusyCount)
				}
				continue
			}

			// Signal the reader via FIFO
			_, err := fifoFile.Write(sigByte)
			elapsed := time.Since(start)
			totalWrites++

			if err != nil {
				fmt.Fprintf(os.Stderr, "[shm_writer] fifo write error #%d: %v\n", totalWrites, err)
				continue
			}

			latencies = append(latencies, elapsed.Microseconds())
			subsystemCounts[sub]++
			totalPayloadBytes += len(pld)

			if totalWrites%100 == 0 {
				fmt.Fprintf(os.Stderr, "[shm_writer] %d writes completed\n", totalWrites)
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
		Role:              "shm_writer",
		TotalWrites:       totalWrites,
		BufferBusyCount:   bufferBusyCount,
		DurationSec:       durationSec,
		IntervalMs:        intervalMs,
		WriteLatencyUs:    latStats,
		WritesBySubsystem: subsystemCounts,
		AvgPayloadBytes:   avgPayload,
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(out))

	fmt.Fprintf(os.Stderr, "[shm_writer] Done: %d writes, %d buffer_busy, avg payload=%d bytes\n",
		totalWrites, bufferBusyCount, avgPayload)
}
