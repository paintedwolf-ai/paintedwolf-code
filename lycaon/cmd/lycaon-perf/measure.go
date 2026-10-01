package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type measurements struct {
	mu     sync.Mutex
	values map[string][]float64
}

func newMeasurements() *measurements {
	return &measurements{values: make(map[string][]float64)}
}

func (m *measurements) add(name string, milliseconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[name] = append(m.values[name], milliseconds)
}

func summarizeMeasurements(measured *measurements) map[string]metricSummary {
	measured.mu.Lock()
	defer measured.mu.Unlock()
	out := make(map[string]metricSummary, len(measured.values))
	for name, samples := range measured.values {
		if len(samples) == 0 {
			continue
		}
		ordered := append([]float64(nil), samples...)
		sort.Float64s(ordered)
		var total float64
		for _, sample := range ordered {
			total += sample
		}
		out[name] = metricSummary{
			Count: len(ordered), MinMS: ordered[0], MeanMS: total / float64(len(ordered)),
			P50MS: percentile(ordered, 0.50), P95MS: percentile(ordered, 0.95),
			P99MS: percentile(ordered, 0.99), MaxMS: ordered[len(ordered)-1],
		}
	}
	return out
}

func percentile(ordered []float64, fraction float64) float64 {
	if len(ordered) == 0 {
		return 0
	}
	index := int(math.Ceil(float64(len(ordered))*fraction)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index]
}

func writeReport(path string, value report) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return replaceFile(path, data, 0o644)
}

func printReport(w io.Writer, value report, path string) {
	fmt.Fprintf(w, "Sidecar performance: %s scale, %d files\n", value.Scale, value.Fixture.Files)
	names := make([]string, 0, len(value.Metrics))
	for name := range value.Metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		metric := value.Metrics[name]
		fmt.Fprintf(w, "  %-32s p50 %8.2f ms  p95 %8.2f ms  max %8.2f ms  n=%d\n",
			name, metric.P50MS, metric.P95MS, metric.MaxMS, metric.Count)
	}
	fmt.Fprintf(w, "  peak RSS %.1f MiB, heap %.1f MiB, goroutines %d, FDs %d\n",
		float64(value.Resources.PeakRSSBytes)/(1<<20), float64(value.Resources.PeakHeapBytes)/(1<<20),
		value.Resources.PeakGoroutines, value.Resources.PeakFDs)
	if len(value.Violations) > 0 {
		fmt.Fprintln(w, "Budget violations:")
		for _, violation := range value.Violations {
			fmt.Fprintf(w, "  - %s\n", violation)
		}
	}
	fmt.Fprintf(w, "Report: %s\n", path)
}
