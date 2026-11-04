package platform_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	lexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

type availableConfinement struct{ err error }

func (c availableConfinement) Available(context.Context, adapter.ProbeConfig) error  { return c.err }
func (availableConfinement) VerifyView(context.Context, *adapter.PreparedView) error { return nil }

func TestSelectionFallsBackWithTheConfinementReasonWithoutEnforcement(t *testing.T) {
	for name, cfg := range map[string]platform.Config{
		"no enforcer":          {},
		"enforcer unavailable": {Confinement: availableConfinement{err: errors.New("sandbox helper missing")}},
	} {
		t.Run(name, func(t *testing.T) {
			selector := platform.NewSelector(cfg)
			chosen, probe, err := selector.Select(t.Context(), adapter.ProbeConfig{RuntimeDir: t.TempDir()})
			testutil.FailErr(t, "Select", err)
			if chosen.Facts().PreparationKind != adapter.PreparationMaterialized {
				t.Fatalf("selected %s without enforcement", chosen.ID())
			}
			if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				return
			}
			if chosen.Facts().FallbackReason != adapter.ReasonConfinementUnavailable || probe.ReasonCode != adapter.ReasonConfinementUnavailable {
				t.Fatalf("fallback reason = %s, probe reason = %s", chosen.Facts().FallbackReason, probe.ReasonCode)
			}
		})
	}
}

// mountedHostView prepares a real view with this host's adapter, through real private Git.
func mountedHostView(t *testing.T, trunk string, mgr *privategit.Manager) (*adapter.PreparedView, adapter.Adapter) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve temp", err)
	selector := platform.NewSelector(platform.Config{Confinement: availableConfinement{}})
	chosen, probe, err := selector.Select(t.Context(), adapter.ProbeConfig{RuntimeDir: filepath.Join(tmp, "probe")})
	testutil.FailErr(t, "Select", err)
	if chosen.Facts().PreparationKind != adapter.PreparationMounted {
		t.Skipf("this host cannot mount a view: %s %s", probe.ReasonCode, probe.Diagnostic)
	}
	view, err := chosen.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: "view-starters", BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: filepath.Join(tmp, "runtime"),
		GitInterceptor: mgr,
		QuiesceHook:    func(context.Context, string) error { return nil }, FlushClientHook: func(context.Context, string) error { return nil },
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "Prepare", err)
	t.Cleanup(func() { _, _ = chosen.Release(context.Background(), view.ViewID) })
	supervisor, ok := chosen.(adapter.MountSupervisor)
	if !ok {
		t.Fatalf("mounted adapter %s has no supervision", chosen.ID())
	}
	testutil.FailErr(t, "BindPrivateGit", supervisor.BindPrivateGit(view.ViewID, "worker-starters", mgr))
	return view, chosen
}

// TestEngineStartersEnterAMountUnderForcedGC runs every real starter against a mount while a
// collection is always pending, on one scheduler slot, repeatedly. The server runs in this process,
// so a child that touched the mount before exec would wait on a server the pending stop-the-world
// has parked. Nothing here is timed: a regression hangs until the test binary's own deadline, and
// the assertion is that every start completed.
func TestEngineStartersEnterAMountUnderForcedGC(t *testing.T) {
	trunk, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve trunk", err)
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(trunk, "README.md"), []byte("hello\n"), 0o644))
	gittest.InitCommit(t, trunk, "initial")
	mgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(t.TempDir(), "pgit")})
	testutil.FailErr(t, "privategit.NewManager", err)
	t.Cleanup(func() { _ = mgr.Store().Close() })
	view, _ := mountedHostView(t, trunk, mgr)
	mount := view.PrimaryWorkPath
	sub := filepath.Join(mount, "sub", "deep")
	testutil.FailErr(t, "mkdir on mount", os.MkdirAll(sub, 0o755))
	testutil.FailErr(t, "write tool on mount", os.WriteFile(filepath.Join(mount, "tool"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	executor := privategit.NewGitExecutor(mgr, view.ViewID, privategit.RemotePolicy{})
	_, err = executor.Status(t.Context())
	testutil.FailErr(t, "initialize private Git", err)

	starters := map[string]func(ctx context.Context) error{
		"gitexec at the mount root": func(ctx context.Context) error {
			return exitOK(gitexec.Run(ctx, mount, []string{"--version"}, gitexec.Opts{}))
		},
		"gitexec below the mount root": func(ctx context.Context) error {
			return exitOK(gitexec.Run(ctx, sub, []string{"--version"}, gitexec.Opts{}))
		},
		"private Git executor": func(ctx context.Context) error {
			_, err := executor.Status(ctx)
			return err
		},
		"launch with a working directory on the mount": func(ctx context.Context) error {
			return exitOK(lexec.Run(ctx, "/bin/sh", []string{"-c", "cat ../../README.md >/dev/null"}, lexec.ExecOpts{Launch: lexec.HostLaunch("view starter"), Dir: sub}))
		},
		"launch of a program on the mount": func(ctx context.Context) error {
			return exitOK(lexec.Run(ctx, filepath.Join(mount, "tool"), nil, lexec.ExecOpts{Launch: lexec.HostLaunch("view starter"), Dir: mount}))
		},
	}

	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	stop := make(chan struct{})
	var collector sync.WaitGroup
	collector.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
			}
		}
	})
	defer func() {
		close(stop)
		collector.Wait()
	}()

	const rounds = 40
	completed := make(map[string]int, len(starters))
	for round := range rounds {
		// A root mutation drops any kernel attribute cache, so the next entry must reach the server.
		testutil.FailErr(t, "mutate mount root", os.WriteFile(filepath.Join(mount, "touch"), []byte{byte(round)}, 0o644))
		for name, start := range starters {
			if err := start(t.Context()); err != nil {
				t.Fatalf("%s, round %d: %v", name, round, err)
			}
			completed[name]++
		}
	}
	for name := range starters {
		if completed[name] != rounds {
			t.Fatalf("%s completed %d of %d starts", name, completed[name], rounds)
		}
	}
}

func exitOK(out []byte, code int, err error) error {
	if err != nil {
		return err
	}
	if code != 0 {
		return errors.New(string(out))
	}
	return nil
}
