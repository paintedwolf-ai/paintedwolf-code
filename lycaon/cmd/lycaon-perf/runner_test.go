package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
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
	t.Setenv("HOME", "")
	if _, err := installedConfigRoot(); err == nil {
		t.Fatal("expected empty HOME to fail installedConfigRoot")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	root, err := installedConfigRoot()
	if err != nil || root != filepath.Join(home, ".config", configdir.DirNameProd) {
		t.Fatalf("fresh account = %q, %v", root, err)
	}
	bin := filepath.Join(t.TempDir(), "sidecar")
	testutil.FailErr(t, "write sidecar script", os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o700))
	cfg := runConfig{binary: bin, scale: "small", iterations: 1, prompts: 2, installed: true}
	state, err := newRunState(cfg)
	testutil.FailErr(t, "newRunState installed", err)
	if state.configDir != root {
		t.Fatalf("state.configDir = %q, want %q", state.configDir, root)
	}
	rep, err := state.buildReport(resourceSummary{})
	testutil.FailErr(t, "buildReport installed", err)
	if rep.Mode != "installed" || rep.Prompts != 0 {
		t.Fatalf("installed report = mode %q prompts %d", rep.Mode, rep.Prompts)
	}
	state.cfg.installed = false
	rep, err = state.buildReport(resourceSummary{})
	testutil.FailErr(t, "buildReport sidecar", err)
	if rep.Mode != "sidecar" || rep.Prompts != 2 {
		t.Fatalf("sidecar report = mode %q prompts %d", rep.Mode, rep.Prompts)
	}
	if _, err := newRunState(cfg); err == nil || !strings.Contains(err.Error(), "disposable account") {
		t.Fatalf("existing configuration = %v", err)
	}
	testutil.FailErr(t, "remove scratch", os.RemoveAll(state.scratch))
	testutil.FailErr(t, "remove configDir", os.RemoveAll(state.configDir))
	testutil.FailErr(t, "chmod sidecar non-executable", os.Chmod(bin, 0o600))
	sidecarCfg := cfg
	sidecarCfg.installed = false
	if _, err := run(t.Context(), sidecarCfg); err == nil {
		t.Fatal("expected non-executable sidecar binary to fail sidecar run")
	}
	if _, err := run(t.Context(), cfg); err == nil {
		t.Fatal("expected non-executable sidecar binary to fail run")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run did not remove installed configDir: %v", err)
	}
}

func TestInstalledWorkloadRunsQueriesWithoutPrompts(t *testing.T) {
	failQueries := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result any
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects":
			result = api.Project{ID: "fixture"}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			result = api.Session{ID: "sess", Status: api.SessionStatusIdle}
		case r.URL.Path == "/v1/sessions/sess":
			result = api.Session{ID: "sess", Status: api.SessionStatusIdle}
		case r.URL.Path == "/v1/sessions/sess/bootstrap":
			result = api.SessionBootstrap{Session: api.Session{ID: "sess", Status: api.SessionStatusIdle}}
		case r.URL.Path == "/v1/events":
			w.WriteHeader(http.StatusOK)
			return
		case r.URL.Path == "/v1/projects/fixture/source/workspace":
			if failQueries {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			result = api.SourceWorkspace{WorkspaceID: "ws", Roots: []api.SourceWorkspaceRoot{{ID: "root"}}}
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
			return
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/presentations"):
			result = api.SourcePresentation{ID: "pres", View: api.SourceView{Tree: &api.SourceTreeView{Kind: "tree", ID: "view", State: "ready", ProjectionRevision: "rev", Extent: api.SourceViewExtent{Rows: 1, Complete: true}}}}
		case r.URL.Query().Get("limit") != "":
			rows := []api.SourceTreeRow{{Kind: "directory", Expanded: true, Address: api.SourceTreeAddress{RootID: "root", Path: "."}}}
			result = api.SourceTreeFrame{Kind: "tree", ViewID: "view", ProjectionRevision: "rev", Span: api.SourceViewSpan{End: 1}, Rows: rows}
		default:
			result = api.SourceTreeView{Kind: "tree", ID: "view", State: "ready", ProjectionRevision: "rev", Extent: api.SourceViewExtent{Rows: 1, Complete: true}}
		}
		testutil.FailErr(t, "encode response", json.NewEncoder(w).Encode(result))
	}))
	defer server.Close()
	state := &runState{
		cfg:       runConfig{iterations: 1, installed: true},
		client:    &sidecarClient{base: server.URL, http: server.Client()},
		resources: &processResourceSampler{pid: os.Getpid()},
		metrics:   newMeasurements(),
	}
	testutil.FailErr(t, "runWorkload installed", state.runWorkload(t.Context()))
	failQueries = true
	if err := state.runWorkload(t.Context()); err == nil {
		t.Fatal("expected query failure in installed workload")
	}
}
