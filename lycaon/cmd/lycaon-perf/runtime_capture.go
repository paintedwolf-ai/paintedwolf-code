package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
)

// meanWaitMS is the average time one blocked connection request waited. The
// pool's wait totals are cumulative, so their ratio does not grow with run length.
func meanWaitMS(gauges map[string]int64, pool string) float64 {
	count := gauges[pool+"_wait_count"]
	if count == 0 {
		return 0
	}
	return float64(gauges[pool+"_wait_ns"]) / float64(count) / 1e6
}

func (s *runState) collectInternalPerformance(resources *resourceSummary) error {
	if s.workloadBaseline.IsZero() || !s.workloadEnd.After(s.workloadBaseline) {
		return fmt.Errorf("performance capture requires a completed workload interval")
	}
	file, err := os.Open(s.perfPath)
	if err != nil {
		return fmt.Errorf("open performance capture: %w", err)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	var firstRuntime, lastRuntime *observability.RuntimeSample
	var firstRuntimeTime, lastRuntimeTime time.Time
	hasRuntime := false
	var descriptorSamples []processResourceSample
	for scanner.Scan() {
		var record observability.PerformanceRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return fmt.Errorf("decode performance record: %w", err)
		}
		if record.Kind == "operation" {
			s.metrics.add("internal."+record.Operation, float64(record.DurationUS)/1000)
		}
		if record.Kind == "capture" && record.Runtime != nil {
			s.correctness.PerfRecordsDropped += record.Runtime.RecordsDropped
		}
		if record.Kind != "runtime" {
			continue
		}
		if record.Time.IsZero() || record.Runtime == nil || record.Runtime.HeapAllocBytes == 0 || record.Runtime.Goroutines <= 0 {
			return fmt.Errorf("performance capture has incomplete runtime measurement")
		}
		for _, name := range []string{
			"db_reader_wait_ns", "db_writer_wait_ns", "db_reader_wait_count", "db_writer_wait_count",
			"db_reader_open", "db_reader_in_use", "db_writer_open", "db_writer_in_use",
		} {
			if value, ok := record.Runtime.Gauges[name]; !ok || value < 0 {
				return fmt.Errorf("performance capture has missing or invalid %s measurement", name)
			}
		}
		if runtime.GOOS == "linux" {
			value, present := record.Runtime.Gauges["process_fds"]
			if !present || value <= 0 || uint64(value) > uint64(^uint(0)>>1) {
				return fmt.Errorf("performance capture has missing or invalid process descriptor measurement")
			}
			resources.PeakFDs = max(resources.PeakFDs, int(value))
			if !record.Time.Before(s.workloadBaseline) && !record.Time.After(s.workloadEnd) {
				descriptorSamples = append(descriptorSamples, processResourceSample{fds: int(value)})
			}
		}
		copy := *record.Runtime
		hasRuntime = true
		if !record.Time.Before(s.workloadBaseline) && !record.Time.After(s.workloadEnd) {
			if firstRuntime == nil {
				firstRuntime = &copy
				firstRuntimeTime = record.Time
			}
			lastRuntime = &copy
			lastRuntimeTime = record.Time
		}
		resources.PeakHeapBytes = max(resources.PeakHeapBytes, copy.HeapAllocBytes)
		resources.PeakGoroutines = max(resources.PeakGoroutines, copy.Goroutines)
		resources.DBReaderMeanWaitMS = max(resources.DBReaderMeanWaitMS, meanWaitMS(copy.Gauges, "db_reader"))
		resources.DBWriterMeanWaitMS = max(resources.DBWriterMeanWaitMS, meanWaitMS(copy.Gauges, "db_writer"))
		resources.PeakDBReaderOpen = max(resources.PeakDBReaderOpen, int(copy.Gauges["db_reader_open"]))
		resources.PeakDBReaderInUse = max(resources.PeakDBReaderInUse, int(copy.Gauges["db_reader_in_use"]))
		resources.PeakDBWriterOpen = max(resources.PeakDBWriterOpen, int(copy.Gauges["db_writer_open"]))
		resources.PeakDBWriterInUse = max(resources.PeakDBWriterInUse, int(copy.Gauges["db_writer_in_use"]))
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read performance capture: %w", err)
	}
	if !hasRuntime {
		return fmt.Errorf("performance capture has no runtime samples")
	}
	if firstRuntime == nil || lastRuntime == nil || !lastRuntimeTime.After(firstRuntimeTime) {
		if s.cfg.soak > 0 {
			return fmt.Errorf("performance capture needs distinct pre-shutdown workload runtime samples for soak growth")
		}
		return nil
	}
	if runtime.GOOS == "linux" {
		resources.FDGrowth = summarizeResourceSamples(descriptorSamples, 0).FDGrowth
	}
	resources.HeapGrowthBytes = uint64Delta(lastRuntime.HeapAllocBytes, firstRuntime.HeapAllocBytes)
	resources.GoroutineGrowth = lastRuntime.Goroutines - firstRuntime.Goroutines
	return nil
}
