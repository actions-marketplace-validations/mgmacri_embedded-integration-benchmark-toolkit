// Package stats provides shared latency statistics computation for Go
// benchmark binaries.
//
// This replaces the LatencyStats/percentile/computeStats code that was
// previously copy-pasted across go_reader, go_writer, sentinel_writer,
// ipc_client, and shm_writer.
//
// Percentile algorithm: floor-index on sorted ascending array.
//
//	data[int(len(data) * pct)]
//
// Matches the C implementation and satisfies copilot-instructions.md.
package stats

import "sort"

// LatencyStats holds the standard percentile breakdown for a latency series.
// All values are in the same unit as the input (typically microseconds).
type LatencyStats struct {
	Min int64 `json:"min"`
	P50 int64 `json:"p50"`
	P95 int64 `json:"p95"`
	P99 int64 `json:"p99"`
	Max int64 `json:"max"`
}

// Percentile computes floor-index percentile on a sorted ascending array.
// pct should be in [0.0, 1.0). The input slice MUST be sorted.
func Percentile(data []int64, pct float64) int64 {
	if len(data) == 0 {
		return 0
	}
	idx := int(float64(len(data)) * pct)
	if idx >= len(data) {
		idx = len(data) - 1
	}
	return data[idx]
}

// Compute sorts the input slice and returns a LatencyStats summary.
// The input slice is sorted in-place.
func Compute(data []int64) LatencyStats {
	if len(data) == 0 {
		return LatencyStats{}
	}
	sort.Slice(data, func(i, j int) bool { return data[i] < data[j] })
	return LatencyStats{
		Min: data[0],
		P50: Percentile(data, 0.50),
		P95: Percentile(data, 0.95),
		P99: Percentile(data, 0.99),
		Max: data[len(data)-1],
	}
}
