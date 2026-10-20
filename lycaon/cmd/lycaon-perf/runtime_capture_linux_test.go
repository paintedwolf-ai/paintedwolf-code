package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxDescriptorCaptureRequiresActualPositiveMeasurement(t *testing.T) {
	for _, value := range []int64{-1, 0, 10} {
		for _, missing := range []bool{false, true} {
			baseline := time.Now()
			sample := completeRuntimeSample()
			sample.Gauges["process_fds"] = value
			if missing {
				delete(sample.Gauges, "process_fds")
			}
			data, err := json.Marshal(observability.PerformanceRecord{Kind: "runtime", Time: baseline, Runtime: sample})
			testutil.FailErr(t, "encode descriptor sample", err)
			path := filepath.Join(t.TempDir(), "capture.jsonl")
			testutil.FailErr(t, "write descriptor sample", os.WriteFile(path, data, 0o600))
			state := &runState{perfPath: path, workloadBaseline: baseline, workloadEnd: baseline.Add(time.Second), metrics: newMeasurements()}
			var resources resourceSummary
			err = state.collectInternalPerformance(&resources)
			if (err == nil) != (!missing && value > 0) {
				t.Fatalf("descriptor value=%d missing=%t: error=%v", value, missing, err)
			}
		}
	}
}

func TestLinuxDescriptorGrowthKeepsStartupPeaksAndExcludesShutdown(t *testing.T) {
	baseline := time.Now()
	var data []byte
	for index, value := range []int64{1000, 10, 20, 1} {
		sample := completeRuntimeSample()
		sample.Gauges["process_fds"] = value
		encoded, err := json.Marshal(observability.PerformanceRecord{Kind: "runtime",
			Time: baseline.Add(time.Duration(index-1) * time.Second), Runtime: sample})
		testutil.FailErr(t, "encode descriptor lifecycle sample", err)
		data = append(append(data, encoded...), '\n')
	}
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	testutil.FailErr(t, "write descriptor lifecycle samples", os.WriteFile(path, data, 0o600))
	state := &runState{perfPath: path, workloadBaseline: baseline,
		workloadEnd: baseline.Add(time.Second), metrics: newMeasurements()}
	var resources resourceSummary
	testutil.FailErr(t, "collect descriptor lifecycle samples", state.collectInternalPerformance(&resources))
	if resources.PeakFDs != 1000 || resources.FDGrowth != 10 {
		t.Fatalf("descriptor lifetime peak=%d workload growth=%d, want 1000 and 10", resources.PeakFDs, resources.FDGrowth)
	}
}
