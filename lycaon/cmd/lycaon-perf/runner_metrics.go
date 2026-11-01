package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

func (s *runState) buildReport(resources resourceSummary) (report, error) {
	var limits budgets
	if err := json.Unmarshal(budgetJSON, &limits); err != nil {
		return report{}, err
	}
	mode, prompts := "sidecar", s.cfg.prompts
	if s.cfg.installed {
		mode, prompts = "installed", 0
	}
	value := report{
		Version: 1, Mode: mode, StartedAt: s.started.UTC(), DurationMS: elapsedMS(s.started),
		Scale: s.cfg.scale, Fixture: s.fixture, Iterations: s.cfg.iterations, Prompts: prompts,
		SoakSeconds: s.cfg.soak.Seconds(), Metrics: summarizeMeasurements(s.metrics), Resources: resources,
		Correctness: s.correctness, Budgets: limits,
	}
	if s.cfg.keep {
		value.ScratchDir = s.scratch
	}
	value.Violations = evaluateBudgets(value)
	return value, nil
}

func evaluateBudgets(value report) []string {
	var violations []string
	for _, name := range sortedMapKeys(value.Budgets.LatencyP95MS) {
		limit := value.Budgets.LatencyP95MS[name]
		metric, ok := value.Metrics[name]
		if !ok {
			if name == "restart.ready" && value.SoakSeconds == 0 {
				continue
			}
			// An installed engine has no mock provider, so prompts are another run's scope.
			if value.Mode == "installed" && strings.HasPrefix(name, "prompt.") {
				continue
			}
			violations = append(violations, fmt.Sprintf("%s was not measured", name))
			continue
		}
		if metric.P95MS > limit {
			violations = append(violations, fmt.Sprintf("%s p95 %.2fms > %.2fms", name, metric.P95MS, limit))
		}
	}
	actual := map[string]float64{
		"peak_rss_bytes": float64(value.Resources.PeakRSSBytes), "rss_growth_bytes": float64(value.Resources.RSSGrowthBytes),
		"peak_heap_bytes": float64(value.Resources.PeakHeapBytes), "heap_growth_bytes": float64(value.Resources.HeapGrowthBytes),
		"peak_goroutines": float64(value.Resources.PeakGoroutines), "goroutine_growth": float64(value.Resources.GoroutineGrowth),
		"peak_fds": float64(value.Resources.PeakFDs), "fd_growth": float64(value.Resources.FDGrowth),
		"db_reader_mean_wait_ms": value.Resources.DBReaderMeanWaitMS, "db_writer_mean_wait_ms": value.Resources.DBWriterMeanWaitMS,
		"peak_db_reader_open":   float64(value.Resources.PeakDBReaderOpen),
		"peak_db_reader_in_use": float64(value.Resources.PeakDBReaderInUse),
		"peak_db_writer_open":   float64(value.Resources.PeakDBWriterOpen),
		"peak_db_writer_in_use": float64(value.Resources.PeakDBWriterInUse),
		"perf_records_dropped":  float64(value.Correctness.PerfRecordsDropped), "http_failures": float64(value.Correctness.HTTPFailures),
	}
	for _, name := range sortedMapKeys(value.Budgets.Resources) {
		limit := value.Budgets.Resources[name]
		if value.SoakSeconds == 0 && strings.Contains(name, "growth") {
			continue
		}
		observed, ok := actual[name]
		if !ok {
			violations = append(violations, fmt.Sprintf("%s budget has no measurement", name))
			continue
		}
		if observed > limit {
			violations = append(violations, fmt.Sprintf("%s %.0f > %.0f", name, observed, limit))
		}
	}
	// Installed runs take no prompt turns, which are what the event stream carries.
	if value.Correctness.EventCount == 0 && value.Mode != "installed" {
		violations = append(violations, "event stream delivered no events")
	}
	if value.Correctness.DuplicateEventIDs > 0 {
		violations = append(violations, fmt.Sprintf("event stream delivered %d duplicate event ids", value.Correctness.DuplicateEventIDs))
	}
	if value.Correctness.PromptTurnsSettled < value.Prompts {
		violations = append(violations, fmt.Sprintf("settled prompt turns %d < requested %d", value.Correctness.PromptTurnsSettled, value.Prompts))
	}
	if value.Correctness.StreamReplaysDone != value.Correctness.PromptTurnsSettled {
		violations = append(violations, fmt.Sprintf("stream replays %d != settled prompts %d", value.Correctness.StreamReplaysDone, value.Correctness.PromptTurnsSettled))
	}
	if value.Correctness.SQLiteQuickCheck != "ok" {
		violations = append(violations, "sqlite quick_check was not ok")
	}
	if value.SoakSeconds > 0 && (value.Correctness.Restarts != 1 || !value.Correctness.RestartRecovered || !value.Correctness.ReplayResetRecovered) {
		violations = append(violations, "soak restart did not recover persisted task state")
	}
	if value.SoakSeconds > 0 && value.Correctness.PostRestartEvents == 0 {
		violations = append(violations, "event stream delivered no events after restart")
	}
	if value.SoakSeconds > 0 && value.Correctness.SoakCycles == 0 {
		violations = append(violations, "soak completed no mixed-workload cycles")
	}
	return violations
}

func sortedMapKeys(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func harnessRunTimeout(soak time.Duration) time.Duration {
	return soak + harnessCompletionGrace
}

func freePort(ctx context.Context) (int, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func randomToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func elapsedMS(start time.Time) float64 { return float64(time.Since(start).Microseconds()) / 1000 }

func quickCheck(ctx context.Context, path string) string {
	database, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err.Error()
	}
	defer func() { _ = database.Close() }()
	var result string
	if err := database.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return err.Error()
	}
	return result
}

func (s runScenarioRunner) runSoak(ctx context.Context, duration time.Duration) error {
	return runSoakCycles(ctx, duration, soakCycleInterval, func() error {
		if err := s.runQueryScenarios(ctx); err != nil {
			return err
		}
		if err := s.runPromptScenarios(ctx, 1); err != nil {
			return err
		}
		s.correctness.SoakCycles++
		return nil
	})
}

func runSoakCycles(ctx context.Context, duration, interval time.Duration, cycle func() error) error {
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if err := cycle(); err != nil {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		timer := time.NewTimer(min(interval, remaining))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}
