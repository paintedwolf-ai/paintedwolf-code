package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPerformanceCaptureRequiresUsableRuntimeEvidence(t *testing.T) {
	baseline := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	record := observability.PerformanceRecord{Kind: "runtime", Time: baseline,
		Runtime: completeRuntimeSample()}
	data, err := json.Marshal(record)
	testutil.FailErr(t, "encode runtime sample", err)
	valid := string(data) + "\n"
	record.Time = baseline.Add(-time.Second)
	oldData, err := json.Marshal(record)
	testutil.FailErr(t, "encode earlier runtime sample", err)
	for _, tc := range []struct {
		name, content  string
		missing, valid bool
	}{
		{name: "missing", missing: true},
		{name: "empty"},
		{name: "malformed suffix", content: valid + "{broken"},
		{name: "scanner failure", content: valid + strings.Repeat("x", 128*1024)},
		{name: "peak sample before workload", content: string(oldData) + "\n", valid: true},
		{name: "usable sample", content: valid, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "performance.jsonl")
			if !tc.missing {
				testutil.FailErr(t, "write performance capture", os.WriteFile(path, []byte(tc.content), 0o600))
			}
			state := &runState{perfPath: path, workloadBaseline: baseline, workloadEnd: baseline.Add(2 * time.Second), metrics: newMeasurements()}
			var resources resourceSummary
			err := state.collectInternalPerformance(&resources)
			if (err == nil) != tc.valid {
				t.Fatalf("capture error = %v; valid = %t", err, tc.valid)
			}
			if tc.valid && (resources.PeakHeapBytes != 100 || resources.PeakGoroutines != 4) {
				t.Fatalf("runtime measurements = %+v", resources)
			}
		})
	}
}

func completeRuntimeSample() *observability.RuntimeSample {
	return &observability.RuntimeSample{
		HeapAllocBytes: 100, Goroutines: 4,
		Gauges: map[string]int64{
			"db_reader_wait_ns": 0, "db_writer_wait_ns": 0, "db_reader_wait_count": 0, "db_writer_wait_count": 0,
			"db_reader_open": 0, "db_reader_in_use": 0, "db_writer_open": 0, "db_writer_in_use": 0,
		},
	}
}

func TestPerformanceCaptureRejectsIncompleteRuntimeSamples(t *testing.T) {
	for _, mutation := range []func(*observability.PerformanceRecord){
		func(r *observability.PerformanceRecord) { r.Time = time.Time{} },
		func(r *observability.PerformanceRecord) { r.Runtime = nil },
		func(r *observability.PerformanceRecord) { r.Runtime = &observability.RuntimeSample{} },
		func(r *observability.PerformanceRecord) { r.Runtime.HeapAllocBytes = 0 },
		func(r *observability.PerformanceRecord) { r.Runtime.Goroutines = 0 },
		func(r *observability.PerformanceRecord) { delete(r.Runtime.Gauges, "db_reader_in_use") },
		func(r *observability.PerformanceRecord) { r.Runtime.Gauges["db_writer_wait_ns"] = -1 },
	} {
		baseline := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
		record := observability.PerformanceRecord{Kind: "runtime", Time: baseline, Runtime: completeRuntimeSample()}
		valid, err := json.Marshal(record)
		testutil.FailErr(t, "encode complete runtime", err)
		mutation(&record)
		invalid, err := json.Marshal(record)
		testutil.FailErr(t, "encode incomplete runtime", err)
		path := filepath.Join(t.TempDir(), "performance.jsonl")
		testutil.FailErr(t, "write partial capture", os.WriteFile(path, append(append(valid, '\n'), invalid...), 0o600))
		state := &runState{perfPath: path, workloadBaseline: baseline, workloadEnd: baseline.Add(2 * time.Second), metrics: newMeasurements()}
		if err := state.collectInternalPerformance(&resourceSummary{}); err == nil {
			t.Fatalf("incomplete sample accepted: %s", invalid)
		}
	}
}

func TestPerformanceSoakRequiresDistinctRuntimeSamples(t *testing.T) {
	for _, elapsed := range []time.Duration{0, time.Second} {
		baseline := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
		record := observability.PerformanceRecord{Kind: "runtime", Time: baseline, Runtime: completeRuntimeSample()}
		first, err := json.Marshal(record)
		testutil.FailErr(t, "encode first runtime", err)
		record.Time = baseline.Add(elapsed)
		record.Runtime.HeapAllocBytes += 20
		record.Runtime.Goroutines += 2
		last, err := json.Marshal(record)
		testutil.FailErr(t, "encode last runtime", err)
		for _, content := range [][]byte{first, append(append(first, '\n'), last...)} {
			path := filepath.Join(t.TempDir(), "performance.jsonl")
			testutil.FailErr(t, "write soak capture", os.WriteFile(path, content, 0o600))
			state := &runState{cfg: runConfig{soak: time.Second}, perfPath: path, workloadBaseline: baseline, workloadEnd: baseline.Add(2 * time.Second), metrics: newMeasurements()}
			var resources resourceSummary
			err := state.collectInternalPerformance(&resources)
			valid := len(content) > len(first) && elapsed > 0
			if (err == nil) != valid {
				t.Fatalf("elapsed=%s: capture error=%v, valid=%v", elapsed, err, valid)
			}
			if valid && (resources.HeapGrowthBytes != 20 || resources.GoroutineGrowth != 2) {
				t.Fatalf("runtime growth=%+v", resources)
			}
		}
	}
}

func TestPerformanceGrowthExcludesShutdownSamples(t *testing.T) {
	baseline := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	for _, secondWorkloadSample := range []bool{false, true} {
		var capture []byte
		for i, at := range []time.Time{baseline.Add(-time.Second), baseline, baseline.Add(time.Second), baseline.Add(3 * time.Second)} {
			if i == 2 && !secondWorkloadSample {
				continue
			}
			sample := completeRuntimeSample()
			sample.HeapAllocBytes = []uint64{1000, 100, 500, 10}[i]
			sample.Goroutines = []int{300, 40, 200, 2}[i]
			data, err := json.Marshal(observability.PerformanceRecord{Kind: "runtime", Time: at, Runtime: sample})
			testutil.FailErr(t, "encode lifecycle sample", err)
			capture = append(append(capture, data...), '\n')
		}
		path := filepath.Join(t.TempDir(), "performance.jsonl")
		testutil.FailErr(t, "write lifecycle capture", os.WriteFile(path, capture, 0o600))
		for _, soak := range []time.Duration{0, time.Minute} {
			state := &runState{cfg: runConfig{soak: soak}, perfPath: path,
				workloadBaseline: baseline, workloadEnd: baseline.Add(2 * time.Second), metrics: newMeasurements()}
			var resources resourceSummary
			err := state.collectInternalPerformance(&resources)
			if !secondWorkloadSample && soak > 0 {
				if err == nil {
					t.Fatal("shutdown supplied missing soak evidence")
				}
				continue
			}
			testutil.FailErr(t, "collect lifecycle measurements", err)
			if resources.PeakHeapBytes != 1000 || resources.PeakGoroutines != 300 {
				t.Fatalf("startup peaks lost: %+v", resources)
			}
			if secondWorkloadSample {
				if resources.HeapGrowthBytes != 400 || resources.GoroutineGrowth != 160 {
					t.Fatalf("shutdown erased workload growth: %+v", resources)
				}
				continue
			}
			data, err := json.Marshal(resources)
			testutil.FailErr(t, "encode peak-only measurements", err)
			var fields map[string]any
			testutil.FailErr(t, "read measurement fields", json.Unmarshal(data, &fields))
			for _, name := range []string{"heap_growth_bytes", "goroutine_growth"} {
				if _, present := fields[name]; present {
					t.Fatalf("insufficient workload evidence published %s: %s", name, data)
				}
			}
		}
	}
}

func TestMeanWaitIgnoresRunLength(t *testing.T) {
	short := map[string]int64{"db_writer_wait_ns": 30_000_000, "db_writer_wait_count": 3}
	long := map[string]int64{"db_writer_wait_ns": 3_000_000_000, "db_writer_wait_count": 300}
	if got, want := meanWaitMS(short, "db_writer"), 10.0; got != want {
		t.Fatalf("short run mean wait = %v, want %v", got, want)
	}
	if meanWaitMS(long, "db_writer") != meanWaitMS(short, "db_writer") {
		t.Fatal("a longer run at the same contention must report the same mean wait")
	}
	if got := meanWaitMS(map[string]int64{"db_writer_wait_ns": 0, "db_writer_wait_count": 0}, "db_writer"); got != 0 {
		t.Fatalf("no waits = %v, want 0", got)
	}
}
