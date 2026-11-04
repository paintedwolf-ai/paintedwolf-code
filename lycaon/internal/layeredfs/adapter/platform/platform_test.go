package platform_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	lexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/workspace"
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
			chosen, probe := overBudgetChoice(t, selector, t.TempDir(), t.TempDir())
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

// overBudgetChoice asks the rule for a provider with the source well past any budget, which is the
// case a mount exists for.
func overBudgetChoice(t *testing.T, selector *adapter.Selector, probeDir, source string) (adapter.Adapter, adapter.ProbeResult) {
	t.Helper()
	selector.BudgetFor = adapter.ZeroBudget
	choice, err := selector.Choose(t.Context(), adapter.ChooseRequest{
		Probe:  adapter.ProbeConfig{RuntimeDir: probeDir},
		Grants: []adapter.RootGrant{{RootID: "primary", SourcePath: source, IsPrimary: true}},
	})
	testutil.FailErr(t, "Choose", err)
	return choice.Adapter, choice.Probe
}

// mountedHostView prepares a real view with this host's adapter, through real private Git.
func mountedHostView(t *testing.T, trunk string, mgr *privategit.Manager) (*adapter.PreparedView, adapter.Adapter) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve temp", err)
	selector := platform.NewSelector(platform.Config{Confinement: availableConfinement{}})
	chosen, probe := overBudgetChoice(t, selector, filepath.Join(tmp, "probe"), trunk)
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

// The fact stands for a kernel client's behavior, so it is checked against a real open.
func TestAdapterFIFOFactMatchesTheKernel(t *testing.T) {
	trunk, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve trunk", err)
	mgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(t.TempDir(), "pgit")})
	testutil.FailErr(t, "privategit.NewManager", err)
	t.Cleanup(func() { _ = mgr.Store().Close() })
	view, chosen := mountedHostView(t, trunk, mgr)
	fifo := filepath.Join(view.PrimaryWorkPath, "pipe")
	testutil.FailErr(t, "mkfifo", syscall.Mkfifo(fifo, 0o644))
	file, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err == nil {
		_ = file.Close()
	}
	opened := err == nil
	if want := chosen.Facts().MetadataCapabilities.SupportsFIFOOpen; opened != want {
		t.Fatalf("SupportsFIFOOpen = %v, kernel open: %v", want, err)
	}
	if !opened && !errors.Is(err, syscall.EACCES) {
		t.Fatalf("FIFO open failed differently from the declared limit: %v", err)
	}
}

// git init of a repository the source does not have is the worker's own and stays in the view.
func TestGitInitOfANewNestedRepositoryStaysPrivate(t *testing.T) {
	trunk, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve trunk", err)
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(trunk, "README.md"), []byte("hello\n"), 0o644))
	gittest.InitCommit(t, trunk, "initial")
	before := gittest.Run(t, trunk, "status", "--porcelain")
	mgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(t.TempDir(), "pgit")})
	testutil.FailErr(t, "privategit.NewManager", err)
	t.Cleanup(func() { _ = mgr.Store().Close() })
	view, _ := mountedHostView(t, trunk, mgr)
	nested := filepath.Join(view.PrimaryWorkPath, "vendor", "new")
	testutil.FailErr(t, "mkdir nested", os.MkdirAll(nested, 0o755))

	testutil.FailErr(t, "git init", exitOK(gitexec.Run(t.Context(), nested, []string{"init", "-q"}, gitexec.Opts{})))
	testutil.FailErr(t, "nested status", exitOK(gitexec.Run(t.Context(), nested, []string{"status", "--porcelain"}, gitexec.Opts{})))
	if info, err := os.Stat(filepath.Join(nested, ".git", "HEAD")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("nested HEAD: %+v err=%v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(trunk, "vendor")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("nested repository reached the source: %v", err)
	}
	if after := gittest.Run(t, trunk, "status", "--porcelain"); after != before {
		t.Fatalf("source repository changed: %q", after)
	}
}

// Each root of a mounted view has its own private repository, and source metadata stays out of reach.
func TestEverySelectedRootHasItsOwnPrivateRepository(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve base", err)
	grants := make([]adapter.RootGrant, 0, 2)
	targets := map[string]string{}
	for _, id := range []string{"p", "s"} {
		source := filepath.Join(base, "source-"+id)
		testutil.FailErr(t, "mkdir source", os.MkdirAll(source, 0o755))
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(source, "file.txt"), []byte(id+"\n"), 0o644))
		gittest.InitCommit(t, source, "initial "+id)
		grants = append(grants, adapter.RootGrant{RootID: id, Label: id, SourcePath: source, IsPrimary: id == "p"})
		targets[id] = filepath.Join(base, "branch", id)
	}
	sourceHead := gittest.Run(t, grants[1].SourcePath, "rev-parse", "HEAD")
	mgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(t.TempDir(), "pgit")})
	testutil.FailErr(t, "privategit.NewManager", err)
	t.Cleanup(func() { _ = mgr.Store().Close() })
	selector := platform.NewSelector(platform.Config{Confinement: availableConfinement{}})
	chosen, probe := overBudgetChoice(t, selector, filepath.Join(base, "probe"), grants[0].SourcePath)
	if chosen.Facts().PreparationKind != adapter.PreparationMounted {
		t.Skipf("this host cannot mount a view: %s %s", probe.ReasonCode, probe.Diagnostic)
	}
	view, err := chosen.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: "view-roots", BackingDir: filepath.Join(base, "backing"), RuntimeDir: filepath.Join(base, "runtime"),
		GitInterceptor: mgr, TargetRootDirs: targets, Grants: grants,
		QuiesceHook: func(context.Context, string) error { return nil }, FlushClientHook: func(context.Context, string) error { return nil },
	})
	testutil.FailErr(t, "Prepare", err)
	t.Cleanup(func() { _, _ = chosen.Release(context.Background(), view.ViewID) })
	testutil.FailErr(t, "BindPrivateGit", chosen.(adapter.MountSupervisor).BindPrivateGit(view.ViewID, "job-roots", mgr))

	secondary, finish, managed, err := privategit.PrepareManagerViewOperation(t.Context(), mgr, targets["s"], "job-roots")
	testutil.FailErr(t, "prepare the secondary root", err)
	if !managed || secondary != targets["s"] {
		t.Fatalf("secondary root is not its own managed view: managed=%v worktree=%s", managed, secondary)
	}
	testutil.FailErr(t, "finish", finish())
	out, code, err := gitexec.Run(t.Context(), targets["s"], []string{"log", "--format=%s"}, gitexec.Opts{})
	if err != nil || code != 0 || !strings.Contains(string(out), "initial s") {
		t.Fatalf("secondary history: code=%d err=%v out=%q", code, err, out)
	}
	testutil.FailErr(t, "branch in the view", exitOK(gitexec.Run(t.Context(), targets["s"], []string{"branch", "private-work"}, gitexec.Opts{})))
	if refs := gittest.Run(t, grants[1].SourcePath, "branch", "--list", "private-work"); refs != "" {
		t.Fatalf("a private branch reached the source: %q", refs)
	}
	if after := gittest.Run(t, grants[1].SourcePath, "rev-parse", "HEAD"); after != sourceHead {
		t.Fatalf("source HEAD moved: %q", after)
	}
	if ids := mgr.SecondaryViews(view.ViewID); len(ids) != 1 {
		t.Fatalf("secondary views = %v", ids)
	}
	testutil.FailErr(t, "release view", mgr.ReleaseView(view.ViewID))
	if ids := mgr.SecondaryViews(view.ViewID); len(ids) != 0 {
		t.Fatalf("release left secondary views: %v", ids)
	}
}

// A manager with no record of the view must still read the kernel's fact: recursive removal through a
// live mount would delete the worker's result.
func TestDestroyRefusesARootStillMountedWhateverItsRecordsSay(t *testing.T) {
	trunk, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve trunk", err)
	mgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(t.TempDir(), "pgit")})
	testutil.FailErr(t, "privategit.NewManager", err)
	t.Cleanup(func() { _ = mgr.Store().Close() })
	view, _ := mountedHostView(t, trunk, mgr)
	result := filepath.Join(view.PrimaryWorkPath, "result.txt")
	testutil.FailErr(t, "write result", os.WriteFile(result, []byte("result\n"), 0o644))

	stranger := workspace.NewManager(t.TempDir())
	err = stranger.DestroyWorkerWorkspace(&workspace.Binding{ID: view.ViewID, Root: view.PrimaryWorkPath})
	if !errors.Is(err, workspace.ErrBranchMounted) {
		t.Fatalf("destroy through a live mount = %v, want ErrBranchMounted", err)
	}
	if raw, err := os.ReadFile(result); err != nil || string(raw) != "result\n" {
		t.Fatalf("refused destroy touched the view: %q err=%v", raw, err)
	}
}

// Bytes no sidecar layout decodes, so only a client's removal can take them away.
var undecodableSidecar = []byte("worker data, not a sidecar\n")

// A client that may remove an early sidecar is allowed to, and any other adapter must keep it. A sidecar
// written after its target stays on every adapter.
func TestSidecarSurvivalFollowsTheAdapterFact(t *testing.T) {
	trunk, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve trunk", err)
	mgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(t.TempDir(), "pgit")})
	testutil.FailErr(t, "privategit.NewManager", err)
	t.Cleanup(func() { _ = mgr.Store().Close() })
	view, chosen := mountedHostView(t, trunk, mgr)
	root := view.PrimaryWorkPath

	early := filepath.Join(root, "._early")
	testutil.FailErr(t, "write early sidecar", os.WriteFile(early, undecodableSidecar, 0o644))
	testutil.FailErr(t, "create target", os.WriteFile(filepath.Join(root, "early"), []byte("target\n"), 0o644))
	if !chosen.Facts().MetadataCapabilities.ClientMayRemoveEarlySidecar {
		if raw, err := os.ReadFile(early); err != nil || string(raw) != string(undecodableSidecar) {
			t.Fatalf("early sidecar on an adapter that keeps it: %q err=%v", raw, err)
		}
	}

	testutil.FailErr(t, "create later target", os.WriteFile(filepath.Join(root, "late"), []byte("target\n"), 0o644))
	late := filepath.Join(root, "._late")
	testutil.FailErr(t, "write late sidecar", os.WriteFile(late, undecodableSidecar, 0o644))
	if raw, err := os.ReadFile(late); err != nil || string(raw) != string(undecodableSidecar) {
		t.Fatalf("sidecar written after its target: %q err=%v", raw, err)
	}
}
