package app

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/app/configuration"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"weak"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func testBuildConfig(t *testing.T, configRoot string) configuration.Config {
	t.Helper()
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	configtest.Overlay(t, map[config.Rel]string{
		config.DistroMCP: "providers:\n  - id: svca\n    command: \"true\"\n    args: []\n    enabled: false\n",
	})
	return configuration.Config{
		DBPath:                    filepath.Join(t.TempDir(), "app-test.db"),
		ListenAddr:                "127.0.0.1:0",
		ConfigRoot:                configRoot,
		TestMCPConnector:          &mcp.MockConnector{Tools: map[string][]*sdkmcp.Tool{"svca": {{Name: "do"}}}},
		TestMCPGlobalOverridePath: filepath.Join(t.TempDir(), "mcp-test.yaml"),
	}
}

func TestBuildFailsWithoutPostureRegistry(t *testing.T) {
	testutil.SkipIfShort(t, "assembles the full app graph")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	configtest.Overlay(t, map[config.Rel]string{config.SessionPostures: "postures: []\n"})

	cfg := testBuildConfig(t, t.TempDir())
	if _, err := Build(t.Context(), cfg); err == nil {
		t.Fatal("expected Build to fail on a posture registry declaring no postures")
	}
}

func TestBuildFailureReleasesInstanceResources(t *testing.T) {
	testutil.SkipIfShort(t, "assembles the full app graph twice")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")

	// An empty posture catalog fails after instance resources are acquired.
	configtest.Overlay(t, map[config.Rel]string{config.SessionPostures: "postures: []\n"})

	cfg := testBuildConfig(t, t.TempDir())
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := Build(t.Context(), cfg)
		if err == nil {
			t.Fatalf("attempt %d: expected Build to fail", attempt)
		}
		if strings.Contains(err.Error(), "already running") {
			t.Fatalf("attempt %d retained the failed build's instance lock: %v", attempt, err)
		}
	}
}

func TestBuildFailsOnForbiddenBundledRule(t *testing.T) {
	testutil.SkipIfShort(t, "full app.Build with shipped config tree")
	badRule := `rules:
  - id: forbidden-rule
    when:
      test_evidence_passed: true
    then:
      deny:
        code: X
        message: y
`
	configtest.Overlay(t, map[config.Rel]string{
		config.PostureRulesDir.Join("forbidden-contract-test.yaml"): badRule,
	})

	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")

	cfg := testBuildConfig(t, t.TempDir())
	_, err := Build(t.Context(), cfg)
	if err == nil {
		t.Fatal("expected Build to fail vocabulary validation")
	}
	if !strings.Contains(err.Error(), "vocabulary") && !strings.Contains(err.Error(), "evidence_passed") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildSucceedsWithoutMockOrProviders(t *testing.T) {
	testutil.SkipIfShort(t, "assembles the full app graph with shipped config")
	t.Setenv("LYCAON_LLM_MOCK", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("FIREWORKS_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LYCAON_API_TOKEN", "test-token")

	cfg := testBuildConfig(t, configlayout.FindModuleRoot())
	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build failed", err)
	t.Cleanup(func() { _ = app.Close() })
}

func TestBuildWithSeparateStoreDirectorySupportsWorkers(t *testing.T) {
	testutil.SkipIfShort(t, "assembles the full app graph with shipped config")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")

	cfg := testBuildConfig(t, configlayout.FindModuleRoot())
	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build failed", err)
	t.Cleanup(func() { _ = app.Close() })

	if app.Server == nil || app.DB == nil || app.Sessions == nil || app.Workflows == nil {
		t.Fatalf("ServeApp = %+v", app)
	}
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, app.DB, testdbseed.DefaultProjectID, projectDir)
	jobID, err := app.Delegations.Queue.Enqueue(t.Context(), wire.WorkerTask{
		Prompt: "fixture", Brief: "fixture", ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: projectDir, Scope: &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"."}},
	})
	testutil.FailErr(t, "enqueue worker", err)
	_, err = app.Delegations.Queue.ClaimNext(t.Context(), worker.ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "store-root-test", ExecutionTarget: wire.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim worker", err)
	job, err := app.Delegations.Queue.ClaimWorkerBranch(t.Context(), jobID)
	testutil.FailErr(t, "claim worker branch under active store", err)
	rel, err := filepath.Rel(filepath.Dir(cfg.DBPath), job.WorkspaceRoot)
	testutil.FailErr(t, "resolve branch against active store", err)
	if !filepath.IsLocal(rel) || !strings.HasPrefix(filepath.ToSlash(rel), "worker-branches/") {
		t.Fatalf("worker branch %q is outside store root %q", job.WorkspaceRoot, filepath.Dir(cfg.DBPath))
	}
}

func TestBuildPreservesExplicitSessionLimits(t *testing.T) {
	testutil.SkipIfShort(t, "assembles the full app graph with shipped config")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	cfg := testBuildConfig(t, configlayout.FindModuleRoot())
	limits := settings.DefaultSessionLimits()
	enabled := false
	limits.CoordinatorLoop = &enabled
	cfg.TestSessionLimits = &limits
	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "build with explicit session limits", err)
	t.Cleanup(func() { _ = app.Close() })
	testdbseed.InsertProjectRoot(t, app.DB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := app.Sessions.Manager.Chats.CreateForProject(t.Context(), testdbseed.DefaultProjectID, wire.SessionPostureBuild)
	testutil.FailErr(t, "create session with explicit limits", err)
	allowed, reason, err := app.CoordinatorRuntime.CoordinatorLoop().Admission.ShouldLoopWake(t.Context(), sess.ID, anchor.PhaseAdvanced)
	testutil.FailErr(t, "evaluate workflow phase wake", err)
	if allowed || reason != "feature_disabled" {
		t.Fatalf("workflow wake allowed=%v reason=%q; explicit disabled loop was replaced by live settings", allowed, reason)
	}
}

func TestBuildStopsWhenShutdownArrivesDuringStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	app, err := Build(ctx, testBuildConfig(t, t.TempDir()))
	if !errors.Is(err, context.Canceled) || app != nil {
		t.Fatalf("Build = %v, %v; want no app and the cancellation", app, err)
	}
	if !strings.Contains(err.Error(), "startup interrupted before observability") {
		t.Fatalf("error = %v, want the step it stopped before", err)
	}
}

func closeBuiltHostWithoutServing(t *testing.T) weak.Pointer[session.Host] {
	t.Helper()
	app, err := Build(t.Context(), testBuildConfig(t, configlayout.FindModuleRoot()))
	testutil.FailErr(t, "build without serving", err)
	host := app.Sessions.Manager
	testutil.FailErr(t, "close without serving", app.Close())
	if host.Runner.Settlement.Disposition(true) != wire.SessionIdleDispositionInterrupted {
		t.Fatal("Close did not mark the host as shutting down")
	}
	return weak.Make(host)
}

func TestBuildCloseWithoutServeReleasesSessionHost(t *testing.T) {
	testutil.SkipIfShort(t, "assembles the full app graph")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	host := closeBuiltHostWithoutServing(t)
	runtime.GC()
	if host.Value() != nil {
		t.Fatal("Close without Serve retained the session host")
	}
}

func closeBuiltHostWithArmedWait(t *testing.T) weak.Pointer[session.Host] {
	t.Helper()
	app, err := Build(t.Context(), testBuildConfig(t, configlayout.FindModuleRoot()))
	testutil.FailErr(t, "build with armed wait", err)
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, app.DB, testdbseed.DefaultProjectID, projectDir)
	sess, err := app.Sessions.Store.Create(t.Context(), wire.CreateSessionRequest{ProjectID: testdbseed.DefaultProjectID, Posture: wire.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create waiting session", err)
	host := app.Sessions.Manager
	waits := host.Coordinator.Runtime.CoordinatorLoop().Waits
	waits.EnterSleep(t.Context(), sess.ID, time.Now().Add(time.Hour), "fixture", []loopwake.WaitTrigger{loopwake.WaitTriggerTimer}, nil, loopwake.SleepMoverHost)
	if !waits.IsSleeping(sess.ID) {
		t.Fatal("coordinator wait was not armed before Close")
	}
	triggers := waits.Triggers(sess.ID)
	if len(triggers) != 1 || triggers[0] != loopwake.WaitTriggerTimer {
		t.Fatalf("armed wait triggers = %v, want timer", triggers)
	}
	testutil.FailErr(t, "close with armed wait", app.Close())
	return weak.Make(host)
}

func TestBuildCloseReleasesHostWithArmedCoordinatorWait(t *testing.T) {
	testutil.SkipIfShort(t, "assembles the full app graph")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	host := closeBuiltHostWithArmedWait(t)
	runtime.GC()
	if host.Value() != nil {
		t.Fatal("Close retained the host through its armed coordinator timer")
	}
}
