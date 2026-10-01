package observability

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPerformanceOperationCapturesPhasesAndRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "performance.jsonl")
	t.Setenv("LYCAON_PERF_DEBUG", "1")
	t.Setenv("LYCAON_PERF_DEBUG_FILE", path)
	closePerformance()
	t.Cleanup(closePerformance)

	op := StartPerformanceOperation("task.prepare", map[string]string{"scale": "medium"})
	op.Mark("store")
	op.End("ok")
	RecordRuntimeSample(map[string]int64{"db_reader_in_use": 2})
	closePerformance()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open performance capture: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var records []PerformanceRecord
	for scanner.Scan() {
		var record PerformanceRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode performance record: %v", err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan performance capture: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if records[0].Operation != "task.prepare" || records[0].Outcome != "ok" || len(records[0].Phases) != 1 {
		t.Fatalf("operation record = %+v", records[0])
	}
	if records[1].Runtime == nil || records[1].Runtime.Gauges["db_reader_in_use"] != 2 {
		t.Fatalf("runtime record = %+v", records[1])
	}
}

func TestStartRuntimeSamplerStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "performance.jsonl")
	t.Setenv("LYCAON_PERF_DEBUG", "1")
	t.Setenv("LYCAON_PERF_DEBUG_FILE", path)
	closePerformance()
	t.Cleanup(closePerformance)

	stop := StartRuntimeSampler(t.Context(), func() map[string]int64 {
		return map[string]int64{"ready": 1}
	})
	stop()
	closePerformance()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read performance capture: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("runtime sampler wrote no records")
	}
}
