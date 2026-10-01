package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type processResourceSample struct {
	rssBytes int64
	fds      int
}

type processResourceSampler struct {
	ctx      context.Context
	pid      int
	mu       sync.Mutex
	samples  []processResourceSample
	baseline int
	err      error
	stop     chan struct{}
	done     chan struct{}
}

func startProcessResourceSampler(ctx context.Context, pid int) *processResourceSampler {
	sampler := &processResourceSampler{ctx: ctx, pid: pid, stop: make(chan struct{}), done: make(chan struct{})}
	go sampler.loop()
	return sampler
}

func (s *processResourceSampler) loop() {
	defer close(s.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	s.capture(s.ctx)
	for {
		select {
		case <-s.stop:
			s.capture(s.ctx)
			return
		case <-ticker.C:
			s.capture(s.ctx)
		}
	}
}

func (s *processResourceSampler) capture(ctx context.Context) {
	rss, rssErr := currentRSSBytes(ctx, s.pid)
	fds, fdErr := currentFDCount(ctx, s.pid)
	s.mu.Lock()
	defer s.mu.Unlock()
	if rssErr != nil || fdErr != nil {
		if s.err == nil {
			s.err = errors.Join(rssErr, fdErr)
		}
		return
	}
	s.samples = append(s.samples, processResourceSample{rssBytes: rss, fds: fds})
}

// Growth starts after warm-up; peaks cover the full process lifetime.
func (s *processResourceSampler) markBaseline(ctx context.Context) {
	s.capture(ctx)
	s.mu.Lock()
	s.baseline = len(s.samples) - 1
	s.mu.Unlock()
}

func (s *processResourceSampler) finish() (resourceSummary, error) {
	close(s.stop)
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return summarizeResourceSamples(s.samples, s.baseline), s.err
}

func summarizeResourceSamples(samples []processResourceSample, baseline int) resourceSummary {
	result := resourceSummary{Samples: len(samples)}
	for _, sample := range samples {
		result.PeakRSSBytes = max(result.PeakRSSBytes, sample.rssBytes)
		result.PeakFDs = max(result.PeakFDs, sample.fds)
	}
	steady := samples[min(max(baseline, 0), len(samples)):]
	if len(steady) > 1 {
		window := min(5, max(1, len(steady)/2))
		result.RSSGrowthBytes = medianRSS(steady[len(steady)-window:]) - medianRSS(steady[:window])
		result.FDGrowth = medianFDs(steady[len(steady)-window:]) - medianFDs(steady[:window])
	}
	return result
}

func mergeResourceSummaries(segments []resourceSummary) resourceSummary {
	var result resourceSummary
	for _, segment := range segments {
		result.Samples += segment.Samples
		result.PeakRSSBytes = max(result.PeakRSSBytes, segment.PeakRSSBytes)
		result.PeakFDs = max(result.PeakFDs, segment.PeakFDs)
		// Growth belongs to the final process segment.
		result.RSSGrowthBytes = segment.RSSGrowthBytes
		result.FDGrowth = segment.FDGrowth
	}
	return result
}

func medianRSS(samples []processResourceSample) int64 {
	values := make([]int64, len(samples))
	for index, sample := range samples {
		values[index] = sample.rssBytes
	}
	slices.Sort(values)
	return values[len(values)/2]
}

func medianFDs(samples []processResourceSample) int {
	values := make([]int, len(samples))
	for index, sample := range samples {
		values[index] = sample.fds
	}
	slices.Sort(values)
	return values[len(values)/2]
}

func uint64Delta(current, baseline uint64) int64 {
	const (
		maxUnsignedDelta uint64 = 1<<63 - 1
		maxSignedDelta   int64  = 1<<63 - 1
	)
	if current >= baseline {
		delta := current - baseline
		if delta > maxUnsignedDelta {
			return maxSignedDelta
		}
		return int64(delta)
	}
	delta := baseline - current
	if delta > maxUnsignedDelta {
		return -maxSignedDelta
	}
	return -int64(delta)
}

func currentRSSBytes(parent context.Context, pid int) (int64, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // PID belongs to the runner's child.
	if err != nil {
		return 0, fmt.Errorf("measure process RSS: %w", err)
	}
	return parseRSSBytes(string(out))
}

func parseRSSBytes(value string) (int64, error) {
	kib, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || kib <= 0 || kib > (1<<63-1)/1024 {
		return 0, fmt.Errorf("invalid process RSS measurement %q", value)
	}
	return kib * 1024, nil
}

func currentFDCount(parent context.Context, pid int) (int, error) {
	if runtime.GOOS == "linux" {
		entries, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
		if err != nil {
			return 0, fmt.Errorf("measure process descriptors: %w", err)
		}
		if len(entries) == 0 {
			return 0, fmt.Errorf("process descriptor measurement is empty")
		}
		return len(entries), nil
	}
	if runtime.GOOS != "darwin" {
		return 0, fmt.Errorf("process descriptor measurement is unsupported on %s", runtime.GOOS)
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-Fn").Output() //nolint:gosec // PID belongs to the runner's child.
	if err != nil {
		return 0, fmt.Errorf("measure process descriptors: %w", err)
	}
	return parseFDCount(string(out))
}

func parseFDCount(output string) (int, error) {
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		value := strings.TrimPrefix(scanner.Text(), "f")
		if value != scanner.Text() && isDecimal(value) {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read process descriptor measurement: %w", err)
	}
	if count == 0 {
		return 0, fmt.Errorf("process descriptor measurement has no numeric descriptors")
	}
	return count, nil
}

func isDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func tailFile(path string, lines int) string {
	data, err := os.ReadFile(path) //nolint:gosec // Path is a runner-created stdout/stderr capture under its scratch root.
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

func processExitedError(path string, err error) error {
	if err == nil {
		return fmt.Errorf("sidecar exited before the workload completed\n%s", tailFile(path, 30))
	}
	return fmt.Errorf("sidecar exited: %w\n%s", err, tailFile(path, 30))
}
