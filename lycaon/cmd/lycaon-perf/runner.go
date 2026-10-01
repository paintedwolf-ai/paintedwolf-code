package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/debugretention"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/pkg/api"
	_ "modernc.org/sqlite"
)

//go:embed budgets.json
var budgetJSON []byte

type runState struct {
	cfg              runConfig
	started          time.Time
	workloadBaseline time.Time
	workloadEnd      time.Time
	scratch          string
	configDir        string
	projectDir       string
	perfPath         string
	stderrPath       string
	dbPath           string
	client           *sidecarClient
	process          *exec.Cmd
	wait             chan error
	resources        *processResourceSampler
	resourceSegments []resourceSummary
	starts           int
	metrics          *measurements
	correctness      correctnessSummary
	fixture          fixtureShape
	project          api.Project
	session          api.Session
}

const (
	soakCycleInterval      = 5 * time.Second
	harnessCompletionGrace = 5 * time.Minute
)

func run(ctx context.Context, cfg runConfig) (result report, resultErr error) {
	state, err := newRunState(cfg)
	if err != nil {
		return report{}, err
	}
	if !cfg.keep {
		defer func() {
			if err := os.RemoveAll(state.scratch); resultErr == nil && err != nil {
				resultErr = fmt.Errorf("remove performance scratch directory: %w", err)
			}
		}()
	}
	runCtx, cancelRun := context.WithTimeout(ctx, harnessRunTimeout(cfg.soak))
	defer cancelRun()
	if err := state.startSidecar(runCtx); err != nil {
		_ = state.stopSidecar()
		return report{}, err
	}
	defer func() {
		if err := state.stopSidecar(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("stop performance sidecar: %w", err)
		}
	}()
	if err := state.runWorkload(runCtx); err != nil {
		return report{}, err
	}
	state.workloadEnd = time.Now()
	if err := state.stopSidecar(); err != nil {
		return report{}, err
	}
	resources := mergeResourceSummaries(state.resourceSegments)
	if err := state.collectInternalPerformance(&resources); err != nil {
		return report{}, err
	}
	state.correctness.SQLiteQuickCheck = quickCheck(ctx, state.dbPath)
	if state.correctness.SQLiteQuickCheck != "ok" {
		return report{}, fmt.Errorf("sqlite quick_check = %q", state.correctness.SQLiteQuickCheck)
	}
	if info, statErr := os.Stat(state.dbPath + "-wal"); statErr == nil {
		resources.WALBytes = info.Size()
	} else if !os.IsNotExist(statErr) {
		return report{}, fmt.Errorf("measure database WAL size: %w", statErr)
	}
	return state.buildReport(resources)
}

func newRunState(cfg runConfig) (*runState, error) {
	binary, err := filepath.Abs(cfg.binary)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(binary); err != nil || info.IsDir() {
		return nil, fmt.Errorf("sidecar binary %q is unavailable", binary)
	}
	cfg.binary = binary
	scratch, err := os.MkdirTemp("", "painted-wolf-sidecar-perf-*")
	if err != nil {
		return nil, err
	}
	performanceFile, ok := debugpaths.Entry(debugpaths.KindPerformance)
	if !ok {
		_ = os.RemoveAll(scratch)
		return nil, fmt.Errorf("performance capture path is not registered")
	}
	state := &runState{
		cfg: cfg, started: time.Now(), scratch: scratch,
		configDir: filepath.Join(scratch, "config"), projectDir: filepath.Join(scratch, "project"),
		perfPath:   filepath.Join(scratch, performanceFile.Name),
		stderrPath: filepath.Join(scratch, "sidecar.stderr.log"),
		metrics:    newMeasurements(),
	}
	state.dbPath = filepath.Join(state.configDir, "store.db")
	if err := os.MkdirAll(state.configDir, 0o700); err != nil {
		_ = os.RemoveAll(scratch)
		return nil, err
	}
	if err := os.MkdirAll(state.projectDir, 0o750); err != nil {
		_ = os.RemoveAll(scratch)
		return nil, err
	}
	state.fixture, err = buildFixture(state.projectDir, cfg.scale)
	if err != nil {
		_ = os.RemoveAll(scratch)
		return nil, err
	}
	return state, nil
}

func (s *runState) startSidecar(ctx context.Context) error {
	port, err := freePort(ctx)
	if err != nil {
		return err
	}
	token, err := randomToken()
	if err != nil {
		return err
	}
	stderr, err := debugretention.OpenFile(s.stderrPath, debugretention.DefaultConfig().MaxFileBytes)
	if err != nil {
		return err
	}
	stdout, err := debugretention.OpenFile(filepath.Join(s.scratch, "sidecar.stdout.log"), debugretention.DefaultConfig().MaxFileBytes)
	if err != nil {
		_ = stderr.Close()
		return err
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	s.process = exec.CommandContext(ctx, s.cfg.binary, "serve", "--db", s.dbPath) //nolint:gosec // Explicit benchmark binary; exec.CommandContext performs no shell parsing.
	s.process.Env = append(os.Environ(),
		"LYCAON_ADDR="+addr,
		"LYCAON_API_TOKEN="+token,
		"LYCAON_CONFIG_DIR="+s.configDir,
		"LYCAON_LLM_MOCK=1",
		"LYCAON_PERF_DEBUG=1",
		"LYCAON_PERF_DEBUG_FILE="+s.perfPath,
		"LYCAON_LOG_LEVEL=info",
	)
	s.process.Stdout, s.process.Stderr = stdout, stderr
	start := time.Now()
	if err := s.process.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return err
	}
	process := s.process
	wait := make(chan error, 1)
	s.wait = wait
	go func() {
		err := process.Wait()
		_ = stdout.Close()
		_ = stderr.Close()
		wait <- err
	}()
	s.resources = startProcessResourceSampler(ctx, s.process.Process.Pid)
	s.starts++
	s.client = &sidecarClient{
		base: "http://" + addr, token: token,
		http: &http.Client{},
	}
	for {
		var health map[string]any
		err := s.client.request(ctx, http.MethodGet, "/health", nil, &health)
		if err == nil && health["status"] == "ok" {
			metric := "startup.ready"
			if s.starts > 1 {
				metric = "restart.ready"
			}
			s.metrics.add(metric, float64(time.Since(start).Microseconds())/1000)
			return nil
		}
		select {
		case processErr := <-s.wait:
			return processExitedError(s.stderrPath, processErr)
		case <-ctx.Done():
			return fmt.Errorf("sidecar did not become healthy: %w\n%s", ctx.Err(), tailFile(s.stderrPath, 30))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (s *runState) runWorkload(ctx context.Context) error {
	if err := s.createProjectAndSession(ctx); err != nil {
		return err
	}
	eventCtx, stopEvents := context.WithCancel(ctx)
	monitor, err := s.client.startEventMonitor(eventCtx, s.project.ID, "")
	if err != nil {
		return err
	}
	if err := s.runPromptScenario(ctx, -1, false); err != nil {
		stopEvents()
		<-monitor.done
		return fmt.Errorf("warm prompt: %w", err)
	}
	s.resources.markBaseline(ctx)
	s.workloadBaseline = time.Now()
	if err := s.runQueryScenarios(ctx); err != nil {
		stopEvents()
		<-monitor.done
		return err
	}
	if err := s.runPromptScenarios(ctx, s.cfg.prompts); err != nil {
		stopEvents()
		<-monitor.done
		return err
	}
	stopEvents()
	<-monitor.done
	cursor, monitorErr := s.addEventMonitor(monitor)
	if monitorErr != nil {
		return monitorErr
	}
	if s.cfg.soak > 0 {
		if strings.TrimSpace(cursor) == "" {
			return fmt.Errorf("event stream produced no restart cursor")
		}
		if err := s.restartAndVerify(ctx); err != nil {
			return err
		}
		eventCtx, stopEvents = context.WithCancel(ctx)
		oldMonitor, replayErr := s.client.startEventMonitor(eventCtx, s.project.ID, cursor)
		if replayErr == nil {
			stopEvents()
			<-oldMonitor.done
			return fmt.Errorf("old event cursor after restart unexpectedly remained available")
		}
		if !errors.Is(replayErr, errEventReplayUnavailable) {
			stopEvents()
			return fmt.Errorf("old event cursor after restart, want replay unavailable: %w", replayErr)
		}
		s.correctness.ReplayResetRecovered = true
		monitor, err = s.client.startEventMonitor(eventCtx, s.project.ID, "")
		if err != nil {
			stopEvents()
			return err
		}
		if err := s.runPromptScenario(ctx, -2, false); err != nil {
			stopEvents()
			<-monitor.done
			return fmt.Errorf("restart warm prompt: %w", err)
		}
		s.resources.markBaseline(ctx)
		s.workloadBaseline = time.Now()
		if err := s.runSoak(ctx, s.cfg.soak); err != nil {
			stopEvents()
			<-monitor.done
			return err
		}
		stopEvents()
		<-monitor.done
		s.correctness.PostRestartEvents = monitor.countSnapshot()
		_, monitorErr = s.addEventMonitor(monitor)
		return monitorErr
	}
	return nil
}

func (s *runState) addEventMonitor(monitor *eventMonitor) (string, error) {
	count, duplicates, cursor, err := monitor.snapshot()
	s.correctness.EventCount += count
	s.correctness.DuplicateEventIDs += duplicates
	return cursor, err
}

func (s *runState) restartAndVerify(ctx context.Context) error {
	if err := s.stopSidecar(); err != nil {
		return fmt.Errorf("stop before restart: %w", err)
	}
	if err := s.startSidecar(ctx); err != nil {
		return fmt.Errorf("restart sidecar: %w", err)
	}
	s.correctness.Restarts++
	bootstrap, err := s.client.bootstrap(ctx, s.session.ID)
	if err != nil {
		return fmt.Errorf("bootstrap after restart: %w", err)
	}
	if bootstrap.Session.ID != s.session.ID || len(bootstrap.Transcript.Messages) == 0 {
		return fmt.Errorf("restart did not recover session %s", s.session.ID)
	}
	s.correctness.RestartRecovered = true
	return nil
}

func (s *runState) createProjectAndSession(ctx context.Context) error {
	started := time.Now()
	err := s.client.request(ctx, http.MethodPost, "/v1/projects", api.CreateProjectRequest{
		Roots: []api.CreateProjectRootInput{{Path: s.projectDir}},
	}, &s.project)
	s.metrics.add("project.create", elapsedMS(started))
	if err != nil {
		return err
	}
	started = time.Now()
	err = s.client.request(ctx, http.MethodPost, "/v1/sessions", api.CreateSessionRequest{
		ProjectID: s.project.ID, Posture: api.SessionPostureBuild,
	}, &s.session)
	s.metrics.add("session.accept", elapsedMS(started))
	if err != nil {
		return err
	}
	started = time.Now()
	if err := s.client.waitPrepared(ctx, s.session.ID); err != nil {
		return err
	}
	s.metrics.add("session.prepare", elapsedMS(started))
	started = time.Now()
	if _, err := s.client.bootstrap(ctx, s.session.ID); err != nil {
		return err
	}
	s.metrics.add("session.bootstrap.cold", elapsedMS(started))
	return nil
}

func (s *runState) runQueryScenarios(ctx context.Context) error {
	for range s.cfg.iterations {
		started := time.Now()
		if _, err := s.client.bootstrap(ctx, s.session.ID); err != nil {
			return err
		}
		s.metrics.add("session.bootstrap.warm", elapsedMS(started))

		if err := s.measureSourceTree(ctx); err != nil {
			return err
		}
	}
	return s.runConcurrentBootstrap(ctx)
}

func (s *runState) runConcurrentBootstrap(ctx context.Context) error {
	const concurrency = 8
	var wg sync.WaitGroup
	errs := make(chan error, concurrency)
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range max(1, s.cfg.iterations/4) {
				started := time.Now()
				if _, err := s.client.bootstrap(ctx, s.session.ID); err != nil {
					errs <- err
					return
				}
				s.metrics.add("session.bootstrap.concurrent", elapsedMS(started))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *runState) runPromptScenarios(ctx context.Context, count int) error {
	for index := range count {
		if err := s.runPromptScenario(ctx, index, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *runState) runPromptScenario(ctx context.Context, index int, measured bool) error {
	bootstrap, err := s.client.bootstrap(ctx, s.session.ID)
	if err != nil {
		return err
	}
	baseline := messageIDs(bootstrap.Transcript.Messages)
	started := time.Now()
	var accepted api.PromptAcceptedResponse
	err = s.client.request(ctx, http.MethodPost, "/v1/sessions/"+s.session.ID+"/prompts", api.PromptRequest{
		OperationID: uuid.NewString(), Text: fmt.Sprintf("performance turn %d", index),
	}, &accepted)
	if measured {
		s.metrics.add("prompt.accept", elapsedMS(started))
	}
	if err != nil {
		return err
	}
	started = time.Now()
	settled, err := s.client.waitPromptSettled(ctx, s.session.ID, baseline)
	if measured {
		s.metrics.add("prompt.settle", elapsedMS(started))
	}
	if err != nil {
		return err
	}
	messageID := newAssistantMessageID(settled.Transcript.Messages, baseline)
	if messageID == "" {
		return fmt.Errorf("prompt turn %d settled without an assistant message", index)
	}
	started = time.Now()
	if err := s.client.replayStream(ctx, s.session.ID, messageID); err != nil {
		return err
	}
	if measured {
		s.metrics.add("prompt.stream_replay", elapsedMS(started))
		s.correctness.PromptTurnsSettled++
		s.correctness.StreamReplaysDone++
	}
	return nil
}

func (s *runState) runSoak(ctx context.Context, duration time.Duration) error {
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

func (s *runState) stopSidecar() (resultErr error) {
	if s.process == nil || s.process.Process == nil {
		return nil
	}
	if s.resources != nil {
		summary, err := s.resources.finish()
		s.resourceSegments = append(s.resourceSegments, summary)
		defer func() { resultErr = errors.Join(resultErr, err) }()
		s.resources = nil
	}
	process := s.process.Process
	s.process = nil
	if err := process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case err := <-s.wait:
		if err != nil {
			return processExitedError(s.stderrPath, err)
		}
		return nil
	case <-time.After(20 * time.Second):
		_ = process.Kill()
		<-s.wait
		return fmt.Errorf("sidecar did not stop within 20s")
	}
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
			"db_reader_wait_ns", "db_writer_wait_ns", "db_reader_open", "db_reader_in_use", "db_writer_open", "db_writer_in_use",
		} {
			if value, ok := record.Runtime.Gauges[name]; !ok || value < 0 {
				return fmt.Errorf("performance capture has missing or invalid %s measurement", name)
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
		resources.DBReaderWaitMS = max(resources.DBReaderWaitMS, float64(copy.Gauges["db_reader_wait_ns"])/1e6)
		resources.DBWriterWaitMS = max(resources.DBWriterWaitMS, float64(copy.Gauges["db_writer_wait_ns"])/1e6)
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
	resources.HeapGrowthBytes = uint64Delta(lastRuntime.HeapAllocBytes, firstRuntime.HeapAllocBytes)
	resources.GoroutineGrowth = lastRuntime.Goroutines - firstRuntime.Goroutines
	return nil
}

func (s *runState) buildReport(resources resourceSummary) (report, error) {
	var limits budgets
	if err := json.Unmarshal(budgetJSON, &limits); err != nil {
		return report{}, err
	}
	value := report{
		Version: 1, StartedAt: s.started.UTC(), DurationMS: elapsedMS(s.started),
		Scale: s.cfg.scale, Fixture: s.fixture, Iterations: s.cfg.iterations, Prompts: s.cfg.prompts,
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
		"db_reader_wait_ms": value.Resources.DBReaderWaitMS, "db_writer_wait_ms": value.Resources.DBWriterWaitMS,
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
	if value.Correctness.EventCount == 0 {
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
