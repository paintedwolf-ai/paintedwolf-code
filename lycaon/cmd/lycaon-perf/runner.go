package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/debugretention"
	"github.com/lycaon/lycaon/pkg/api"
	_ "modernc.org/sqlite"
)

//go:embed budgets.json
var budgetJSON []byte

type runState struct {
	cfg              runConfig
	perfPath         string
	workloadBaseline time.Time
	workloadEnd      time.Time
	client           *sidecarClient
	resources        *processResourceSampler
	metrics          *measurements
	project          api.Project
	fixture          fixtureShape
	runProcessState
}

type runProcessState struct {
	started    time.Time
	scratch    string
	configDir  string
	projectDir string
	stderrPath string
	dbPath     string
	runSessionState
}

type runSessionState struct {
	process          *exec.Cmd
	wait             chan error
	resourceSegments []resourceSummary
	starts           int
	correctness      correctnessSummary
	session          api.Session
}

type runExecutor struct {
	*runState
}

type runScenarioRunner struct {
	*runState
}

func (s *runState) exec() runExecutor {
	return runExecutor{s}
}

func (s *runState) scenarios() runScenarioRunner {
	return runScenarioRunner{s}
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
			// The disposable account had no release configuration before this run.
			if cfg.installed {
				if err := os.RemoveAll(state.configDir); resultErr == nil && err != nil {
					resultErr = fmt.Errorf("remove the run's release configuration: %w", err)
				}
			}
		}()
	}
	runCtx, cancelRun := context.WithTimeout(ctx, harnessRunTimeout(cfg.soak))
	defer cancelRun()
	if err := state.exec().startSidecar(runCtx); err != nil {
		_ = state.exec().stopSidecar()
		return report{}, err
	}
	defer func() {
		if err := state.exec().stopSidecar(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("stop performance sidecar: %w", err)
		}
	}()
	if err := state.runWorkload(runCtx); err != nil {
		return report{}, err
	}
	state.workloadEnd = time.Now()
	if err := state.exec().stopSidecar(); err != nil {
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
	configDir := filepath.Join(scratch, "config")
	if cfg.installed {
		if configDir, err = installedConfigRoot(); err != nil {
			_ = os.RemoveAll(scratch)
			return nil, err
		}
	}
	state := &runState{
		cfg:      cfg,
		perfPath: filepath.Join(scratch, performanceFile.Name),
		metrics:  newMeasurements(),
		runProcessState: runProcessState{
			started:    time.Now(),
			scratch:    scratch,
			configDir:  configDir,
			projectDir: filepath.Join(scratch, "project"),
			stderrPath: filepath.Join(scratch, "sidecar.stderr.log"),
		},
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

// installedConfigRoot is the release configuration an installed engine uses, which must
// not exist yet: release builds ignore configuration overrides, so the run owns the
// account's real configuration and removes it afterwards.
func installedConfigRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, ".config", configdir.DirNameProd)
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("installed mode needs a disposable account without %s", root)
	}
	return root, nil
}

func (s runExecutor) startSidecar(ctx context.Context) error {
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
		"LYCAON_PERF_DEBUG=1",
		"LYCAON_PERF_DEBUG_FILE="+s.perfPath,
		"LYCAON_LOG_LEVEL=info",
	)
	// A release engine ignores both, so an installed run sets neither.
	if !s.cfg.installed {
		s.process.Env = append(s.process.Env, "LYCAON_CONFIG_DIR="+s.configDir, "LYCAON_LLM_MOCK=1")
	}
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

func (s runExecutor) stopSidecar() (resultErr error) {
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
