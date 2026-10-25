package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
)

func healthyReport() report {
	return report{
		Prompts: 2,
		Metrics: map[string]metricSummary{"startup.ready": {P95MS: 10}},
		Correctness: correctnessSummary{
			EventCount: 4, PromptTurnsSettled: 2, StreamReplaysDone: 2, SQLiteQuickCheck: "ok",
		},
		Budgets: budgets{
			LatencyP95MS: map[string]float64{"startup.ready": 20, "restart.ready": 20},
			Resources:    map[string]float64{"rss_growth_bytes": 1},
		},
		Resources: resourceSummary{RSSGrowthBytes: 100},
	}
}

func TestSoakCyclesWaitBetweenCompletedCycles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var cycles atomic.Int32
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runSoakCycles(ctx, time.Hour, time.Hour, func() error {
			cycles.Add(1)
			close(started)
			return nil
		})
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("runSoakCycles error = %v, want context canceled", err)
	}
	if got := cycles.Load(); got != 1 {
		t.Fatalf("cycles before interval = %d, want 1", got)
	}
}

func TestHarnessTimeoutIncludesRequestedSoakAndCompletionGrace(t *testing.T) {
	const soak = 30 * time.Minute
	if got, want := harnessRunTimeout(soak), soak+harnessCompletionGrace; got != want {
		t.Fatalf("harness timeout = %s, want %s", got, want)
	}
}

func TestQuickBudgetSkipsGrowthAndRestartOnlyChecks(t *testing.T) {
	value := healthyReport()
	if got := evaluateBudgets(value); len(got) != 0 {
		t.Fatalf("quick violations = %v", got)
	}
}

func TestSoakBudgetRequiresRecoveredRestart(t *testing.T) {
	value := healthyReport()
	value.SoakSeconds = 10
	value.Metrics["restart.ready"] = metricSummary{P95MS: 10}
	value.Resources.RSSGrowthBytes = 0
	value.Correctness.PostRestartEvents = 1
	value.Correctness.SoakCycles = 1
	violations := evaluateBudgets(value)
	if len(violations) != 1 || !strings.Contains(violations[0], "restart") {
		t.Fatalf("soak violations = %v", violations)
	}
	value.Correctness.Restarts = 1
	value.Correctness.RestartRecovered = true
	value.Correctness.ReplayResetRecovered = true
	if got := evaluateBudgets(value); len(got) != 0 {
		t.Fatalf("recovered soak violations = %v", got)
	}
}

func TestPeakResourceBudgetIsEnforcedInQuickRuns(t *testing.T) {
	value := healthyReport()
	value.Budgets.Resources["peak_fds"] = 10
	value.Resources.PeakFDs = 11
	violations := evaluateBudgets(value)
	if len(violations) != 1 || !strings.Contains(violations[0], "peak_fds") {
		t.Fatalf("peak resource violations = %v", violations)
	}
}

func TestUnknownResourceBudgetCannotPassAsZero(t *testing.T) {
	value := healthyReport()
	value.Budgets.Resources["misspelled_measurement"] = 1
	violations := evaluateBudgets(value)
	if len(violations) != 1 || !strings.Contains(violations[0], "has no measurement") {
		t.Fatalf("unknown resource violations = %v", violations)
	}
}

func TestInstalledRunsAreHeldToReleaseValidBudgets(t *testing.T) {
	value := healthyReport()
	value.Mode, value.Prompts = "installed", 0
	value.Correctness = correctnessSummary{SQLiteQuickCheck: "ok"}
	value.Budgets.LatencyP95MS["prompt.settle"] = 100
	if got := evaluateBudgets(value); len(got) != 0 {
		t.Fatalf("installed violations = %v", got)
	}
	delete(value.Metrics, "startup.ready")
	if got := evaluateBudgets(value); len(got) != 1 || got[0] != "startup.ready was not measured" {
		t.Fatalf("installed run without startup = %v", got)
	}
	value = healthyReport()
	value.Budgets.LatencyP95MS["prompt.settle"] = 100
	if got := evaluateBudgets(value); len(got) != 1 || got[0] != "prompt.settle was not measured" {
		t.Fatalf("sidecar run without prompts = %v", got)
	}
	value.Correctness.EventCount = 0
	if got := evaluateBudgets(value); len(got) != 2 {
		t.Fatalf("sidecar run without events = %v", got)
	}
}

func TestInstalledModeRequiresAnAccountWithoutReleaseConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root, err := installedConfigRoot()
	if err != nil || root != filepath.Join(home, ".config", configdir.DirNameProd) {
		t.Fatalf("fresh account = %q, %v", root, err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if _, err := installedConfigRoot(); err == nil || !strings.Contains(err.Error(), "disposable account") {
		t.Fatalf("existing configuration = %v", err)
	}
}
