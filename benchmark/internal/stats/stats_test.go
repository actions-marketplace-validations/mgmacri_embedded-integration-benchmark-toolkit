package stats

import "testing"

func TestPercentileEmpty(t *testing.T) {
	if got := Percentile(nil, 0.50); got != 0 {
		t.Errorf("Percentile(nil, 0.50) = %d, want 0", got)
	}
}

func TestPercentileSingleElement(t *testing.T) {
	data := []int64{42}
	if got := Percentile(data, 0.50); got != 42 {
		t.Errorf("Percentile([42], 0.50) = %d, want 42", got)
	}
	if got := Percentile(data, 0.99); got != 42 {
		t.Errorf("Percentile([42], 0.99) = %d, want 42", got)
	}
}

func TestPercentileFloorIndex(t *testing.T) {
	// 10 elements: indices 0-9
	// p50 → floor(10*0.50) = 5 → data[5]
	// p95 → floor(10*0.95) = 9 → data[9]
	// p99 → floor(10*0.99) = 9 → data[9]
	data := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}

	tests := []struct {
		pct  float64
		want int64
	}{
		{0.00, 10},  // floor(10*0.00) = 0
		{0.50, 60},  // floor(10*0.50) = 5
		{0.95, 100}, // floor(10*0.95) = 9
		{0.99, 100}, // floor(10*0.99) = 9
	}

	for _, tt := range tests {
		got := Percentile(data, tt.pct)
		if got != tt.want {
			t.Errorf("Percentile(10 elems, %.2f) = %d, want %d", tt.pct, got, tt.want)
		}
	}
}

func TestCompute(t *testing.T) {
	// Unsorted input — Compute should sort it
	data := []int64{500, 100, 300, 200, 400}
	s := Compute(data)

	if s.Min != 100 {
		t.Errorf("Min = %d, want 100", s.Min)
	}
	if s.Max != 500 {
		t.Errorf("Max = %d, want 500", s.Max)
	}
	// p50 → floor(5*0.50) = 2 → data[2] = 300 (after sort)
	if s.P50 != 300 {
		t.Errorf("P50 = %d, want 300", s.P50)
	}
}

func TestComputeEmpty(t *testing.T) {
	s := Compute(nil)
	if s.Min != 0 || s.P50 != 0 || s.P95 != 0 || s.P99 != 0 || s.Max != 0 {
		t.Errorf("Compute(nil) should return zero stats, got %+v", s)
	}
}

func TestComputeLargeDataset(t *testing.T) {
	// 1000 elements: 1..1000
	data := make([]int64, 1000)
	for i := range data {
		data[i] = int64(i + 1)
	}

	s := Compute(data)

	if s.Min != 1 {
		t.Errorf("Min = %d, want 1", s.Min)
	}
	if s.Max != 1000 {
		t.Errorf("Max = %d, want 1000", s.Max)
	}
	// p50 → floor(1000*0.50) = 500 → data[500] = 501
	if s.P50 != 501 {
		t.Errorf("P50 = %d, want 501", s.P50)
	}
	// p99 → floor(1000*0.99) = 990 → data[990] = 991
	if s.P99 != 991 {
		t.Errorf("P99 = %d, want 991", s.P99)
	}
}
