package worker

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/denialprobe"
	"github.com/lycaon/lycaon/internal/enginepaths"
	lexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/viewconfine"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// fixtureConfinement stands in for enforcement around the fixture MountedAdapter. It proves nothing
// about denial; those tests exercise claim sequencing, not confinement.
type fixtureConfinement struct{}

func (fixtureConfinement) Available(context.Context, adapter.ProbeConfig) error    { return nil }
func (fixtureConfinement) VerifyView(context.Context, *adapter.PreparedView) error { return nil }

func differentDevice(path string) (bool, error) {
	self, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	selfSys, selfOK := self.Sys().(*syscall.Stat_t)
	parentSys, parentOK := parent.Sys().(*syscall.Stat_t)
	if !selfOK || !parentOK {
		return false, errors.New("device identity unavailable")
	}
	return selfSys.Dev != parentSys.Dev, nil
}

type layeredWorkerFixture struct {
	queue    *SQLQueue
	manager  *workspace.Manager
	source   string
	branches string
}

func newLayeredWorkerFixture(t *testing.T, selector *adapter.Selector) *layeredWorkerFixture {
	t.Helper()
	source, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve source", err)
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "write removable source", os.WriteFile(filepath.Join(source, "gone.go"), []byte("package gone\n"), 0o644))
	gittest.InitCommit(t, source, "initial")
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, sqlDB, testdbseed.DefaultProjectID, "root-id", source)
	branches := filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches")
	privateGit, err := workspace.OpenPrivateGit(branches)
	testutil.FailErr(t, "open private Git", err)
	t.Cleanup(func() { _ = privateGit.Store().Close() })
	manager := workspace.NewManager(branches, t.TempDir()).WithPrivateGit(privateGit)
	if selector != nil {
		manager = manager.WithSelector(selector)
	}
	queue := NewSQLQueue(sqlDB, 4)
	queue.SetBaselineStore(sourceledger.New(sqlDB, filepath.Join(testbaseline.DataDir(t, sqlDB), "source-content")).Baselines)
	queue.SetWorkerWorkspaceManager(manager)
	return &layeredWorkerFixture{queue: queue, manager: manager, source: source, branches: branches}
}

// claim runs the ordinary production path: enqueue, ClaimNext, ClaimWorkerBranch.
func (f *layeredWorkerFixture) claim(t *testing.T) *api.WorkerTask {
	t.Helper()
	id, err := f.queue.Enqueue(t.Context(), api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: f.source, WorkspaceRootID: "root-id",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"main.go"}},
		AgentType: "implementer", Prompt: "Update main.go", Brief: "Update main.go",
	})
	testutil.FailErr(t, "Enqueue", err)
	next, err := f.queue.ClaimNext(t.Context(), ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "ClaimNext", err)
	claimed, err := f.queue.ClaimWorkerBranch(t.Context(), id)
	testutil.FailErr(t, "ClaimWorkerBranch", err)
	claimed.ClaimToken = next.ClaimToken
	return claimed
}

func surveyedPaths(t *testing.T, branchRoot string) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	err := workspace.OverlaySurveyWalk(t.Context(), branchRoot, sandbox.SurveyOptions{}, func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
		found[entry.Rel] = true
		return sandbox.SurveyContinue, nil
	})
	testutil.FailErr(t, "survey branch", err)
	return found
}

// Fixture evidence for the mounted sequence: the adapter has no kernel mount. It runs on every host,
// and pins what a claim hands consumers and what settlement takes back.
func TestOrdinaryWorkerClaimRegistersAndSettlesItsConsumerEngine(t *testing.T) {
	mounted := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: adapter.AdapterFUSELinux,
		MountProbeFn: func(context.Context, string, string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonNone, "", nil
		},
	})
	selector := adapter.NewSelector("linux", mounted, nil, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
	selector.RequireConfinement, selector.Confinement = true, fixtureConfinement{}
	fixture := newLayeredWorkerFixture(t, selector)
	task := fixture.claim(t)

	engine, ok := consumer.LookupBranchEngine(task.WorkspaceRoot)
	if !ok || engine == nil {
		t.Fatalf("claim registered no consumer engine for %s", task.WorkspaceRoot)
	}
	claimFile := filepath.Join(fixture.branches, ".adapter", "recovery", "retention", "claim-view-"+task.ID+".json")
	if _, err := os.Stat(claimFile); err != nil {
		t.Fatalf("claim registered no durable retention: %v", err)
	}
	if won, err := fixture.queue.Complete(t.Context(), task, api.WorkerResult{Status: "complete"}); err != nil || !won {
		t.Fatalf("Complete: won=%v err=%v", won, err)
	}
	if _, ok := consumer.LookupBranchEngine(task.WorkspaceRoot); ok {
		t.Fatal("completing an unchanged worker left its engine registered")
	}
	if _, err := os.Stat(claimFile); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("completing an unchanged worker left its retention claim: %v", err)
	}
}

func TestOrdinaryWorkerClaimFallsBackWithTheConfinementReason(t *testing.T) {
	fixture := newLayeredWorkerFixture(t, nil)
	task := fixture.claim(t)

	snap, receipt, ok := WorkerClaimWorkspaceState(task.ID)
	if !ok || snap.Readiness != claim.StateReady {
		t.Fatalf("claim state ok=%v snap=%+v", ok, snap)
	}
	if snap.Adapter.AdapterID != adapter.AdapterEagerCopy || snap.Adapter.FallbackReason != adapter.ReasonConfinementUnavailable || receipt.AdapterID != adapter.AdapterEagerCopy {
		t.Fatalf("selection did not fall back with the confinement reason: %+v", snap.Adapter)
	}
	if _, ok := consumer.LookupBranchEngine(task.WorkspaceRoot); ok {
		t.Fatal("a materialized branch has no layered engine")
	}
	if !surveyedPaths(t, task.WorkspaceRoot)["main.go"] {
		t.Fatal("survey of the materialized branch misses main.go")
	}

	worktree, finish, managed, err := privategit.PrepareViewGitOperation(t.Context(), task.WorkspaceRoot, task.ID)
	testutil.FailErr(t, "prepare worker Git", err)
	if !managed || worktree != task.WorkspaceRoot {
		t.Fatalf("materialized branch is not a managed view: managed=%v worktree=%s", managed, worktree)
	}
	testutil.FailErr(t, "finish worker Git", finish())
	if out := gittest.Run(t, worktree, "log", "--format=%s"); !strings.Contains(out, "initial") {
		t.Fatalf("private repository misses the source's history: %q", out)
	}

	testutil.FailErr(t, "edit branch", os.WriteFile(filepath.Join(task.WorkspaceRoot, "main.go"), []byte("package changed\n"), 0o644))
	if won, err := fixture.queue.Complete(t.Context(), task, api.WorkerResult{Status: "complete"}); err != nil || !won {
		t.Fatalf("Complete: won=%v err=%v", won, err)
	}
	completed, ok := fixture.queue.Get(task.ID)
	if !ok || completed.MergeStatus != api.WorkerMergeStatusPending {
		t.Fatalf("changed worker is not pending review: %+v", completed)
	}
	if raw, err := os.ReadFile(filepath.Join(fixture.source, "main.go")); err != nil || string(raw) != "package main\n" {
		t.Fatalf("source changed before promotion: %q err=%v", raw, err)
	}
	testutil.FailErr(t, "destroy branch", fixture.manager.DestroyWorkerWorkspace(&workspace.Binding{ID: task.ID, Root: task.WorkspaceRoot}))
}

// The materialized path is the default selector's: a worker reads its pristine base and never writes
// it, while its own tree stays writable.
func TestMaterializedWorkerIsDeniedWritesToItsPristineTree(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	if !confine.Available() {
		t.Skip("this host applies no confinement")
	}
	fixture := newLayeredWorkerFixture(t, nil)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Dir(fixture.branches))
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	task := fixture.claim(t)
	root := task.WorkspaceRoot
	binding := &workspace.Binding{ID: task.ID, Root: root}
	t.Cleanup(func() { _ = fixture.manager.DestroyWorkerWorkspace(binding) })
	snap, _, ok := WorkerClaimWorkspaceState(task.ID)
	if !ok || snap.Adapter.PreparationKind != adapter.PreparationMaterialized {
		t.Fatalf("the default selector chose: ok=%v %+v", ok, snap.Adapter)
	}
	pristine := enginepaths.PristineTreeForBranchRoot(root)
	testutil.FailErr(t, "mkdir pristine", os.MkdirAll(pristine, 0o700))
	base := filepath.Join(pristine, "main.go")
	testutil.FailErr(t, "write base", os.WriteFile(base, []byte("package main\n"), 0o600))

	tctx := tools.ToolContext{}
	tctx.Source.WorkerBranchRoot = root
	tctx.Source.WorkerSourceRoots = []string{fixture.source}
	tctx.Source.WorkerViewBoundary = workspace.BranchViewBoundary(root)
	box, ok := confine.DefaultConfinement(hitl.ActionConfineRequest(tools.ActionConfineInputsForContext(tctx, nil)))
	if !ok {
		t.Fatal("a worker command got no boundary")
	}
	results, err := viewconfine.Launch(t.Context(), box, root, []denialprobe.Step{
		{Kind: denialprobe.ReadFile, Path: base},
		{Kind: denialprobe.CreateFile, Path: filepath.Join(root, "worker-probe")},
		{Kind: denialprobe.OverwriteFile, Path: base},
		{Kind: denialprobe.CreateFile, Path: filepath.Join(pristine, "worker-probe")},
	})
	testutil.FailErr(t, "launch worker probe", err)
	for i, result := range results {
		if i < 2 && !result.Succeeded() {
			t.Fatalf("a worker could not %s %s: errno %d", result.Kind, result.Path, result.Errno)
		}
		if i >= 2 && !result.Denied() {
			t.Fatalf("a worker was not refused %s %s: errno %d", result.Kind, result.Path, result.Errno)
		}
	}
}

// Runs on a real kernel mount through the host's selector, its production enforcer and real services.
// Only a host with no way to mount skips; a host that mounts but does not enforce fails here.
func TestOrdinaryWorkerClaimThroughARealMountedViewToCompletion(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	if !confine.Available() {
		t.Skip("this host applies no confinement")
	}
	fixture := newLayeredWorkerFixture(t, workspace.EnforcedWorkspaceSelector())
	// Worker storage sits under the engine's state root, as it does in production.
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Dir(fixture.branches))
	task := fixture.claim(t)
	snap, _, ok := WorkerClaimWorkspaceState(task.ID)
	if !ok {
		t.Fatal("claim recorded no workspace state")
	}
	if snap.Adapter.PreparationKind != adapter.PreparationMounted {
		switch snap.Adapter.FallbackReason {
		case adapter.ReasonFUSEDeviceUnavailable, adapter.ReasonFUSEHelperUnavailable, adapter.ReasonUnsupportedPlatform:
			t.Skipf("this host cannot mount a view: %s", snap.Adapter.FallbackReason)
		}
		t.Fatalf("an enforcing host fell back: %s", snap.Adapter.FallbackReason)
	}
	destroyed := false
	binding := &workspace.Binding{ID: task.ID, Root: task.WorkspaceRoot}
	t.Cleanup(func() {
		if !destroyed {
			_ = fixture.manager.DestroyWorkerWorkspace(binding)
		}
	})
	if snap.Readiness != claim.StateReady || snap.GitInit != claim.GitInitLazyPending {
		t.Fatalf("mounted claim state: %+v", snap)
	}
	root := task.WorkspaceRoot
	if mounted, err := differentDevice(root); err != nil || !mounted {
		t.Fatalf("branch root is not a kernel mount: mounted=%v err=%v", mounted, err)
	}

	engine, ok := consumer.LookupBranchEngine(root)
	if !ok {
		t.Fatal("claim registered no consumer engine")
	}
	view, cvOK := workspace.LookupBranchClaimedView(root)
	if !cvOK {
		t.Fatal("claim recorded no claimed view")
	}
	rootID := view.Prepared.PrimaryRootID
	for name, path := range map[string]string{
		"backing store": filepath.Join(view.Prepared.BackingDir, "journal"),
		"source tree":   filepath.Join(fixture.source, "main.go"),
	} {
		if err := engine.GuardLiveConsumerPath(rootID, path); !errors.Is(err, consumer.ErrDirectBackingAccessForbidden) {
			t.Fatalf("%s reachable by a live consumer: %v", name, err)
		}
	}

	assertWorkerProcessIsDenied(t, root, fixture.source, view.Prepared)

	testutil.FailErr(t, "create on mount", os.WriteFile(filepath.Join(root, "added.go"), []byte("package added\n"), 0o644))
	testutil.FailErr(t, "edit on mount", os.WriteFile(filepath.Join(root, "main.go"), []byte("package changed\n"), 0o644))
	testutil.FailErr(t, "remove on mount", os.Remove(filepath.Join(root, "gone.go")))

	paths := surveyedPaths(t, root)
	if !paths["added.go"] || paths["gone.go"] {
		t.Fatalf("survey does not reflect private changes: %v", paths)
	}
	evidence, err := engine.ReadEvidence(t.Context(), layeredfs.QualifiedPath{RootID: rootID, Rel: "main.go"})
	testutil.FailErr(t, "source read", err)
	if string(evidence.Content) != "package changed\n" || evidence.Source != consumer.AccessSourceMountedView {
		t.Fatalf("source read = %q from %s", evidence.Content, evidence.Source)
	}

	worktree, finish, managed, err := privategit.PrepareViewGitOperation(t.Context(), root, task.ID)
	testutil.FailErr(t, "prepare worker Git", err)
	if !managed {
		t.Fatal("mounted branch is not a managed Git view")
	}
	out, code, err := gitexec.Run(t.Context(), worktree, []string{"log", "--format=%s"}, gitexec.Opts{})
	if err != nil || code != 0 || !strings.Contains(string(out), "initial") {
		t.Fatalf("private Git on the mount: code=%d err=%v out=%q", code, err, out)
	}
	assertWorkerShellGitWorks(t, root, fixture.source, worktree)
	testutil.FailErr(t, "finish worker Git", finish())
	if _, err := os.Stat(filepath.Join(fixture.source, "added.go")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("private file reached the source: %v", err)
	}

	if won, err := fixture.queue.Complete(t.Context(), task, api.WorkerResult{Status: "complete"}); err != nil || !won {
		t.Fatalf("Complete: won=%v err=%v", won, err)
	}
	completed, ok := fixture.queue.Get(task.ID)
	if !ok || completed.MergeStatus != api.WorkerMergeStatusPending {
		t.Fatalf("changed worker is not pending review: %+v", completed)
	}
	_, manifest, published, err := session.OpenTaskCheckpointStore(completed)
	testutil.FailErr(t, "open task checkpoint store", err)
	captured := map[string]bool{}
	for _, entry := range manifest.Entries {
		captured[entry.Path.Rel] = true
	}
	if !published || !captured["added.go"] || !captured["main.go"] || !captured["gone.go"] {
		t.Fatalf("completion published no checkpoint of the three changes: published=%v entries=%v", published, captured)
	}

	testutil.FailErr(t, "destroy branch", fixture.manager.DestroyWorkerWorkspace(binding))
	destroyed = true
	if _, ok := consumer.LookupBranchEngine(root); ok {
		t.Fatal("settlement left the engine registered")
	}
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("settlement left the branch root: %v", err)
	}
	if _, err := os.Stat(enginepaths.MetaDirForBranchRoot(root)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("settlement left branch metadata: %v", err)
	}
}

func workerCommandBox(t *testing.T, root, source string) *confine.Confinement {
	t.Helper()
	// No egress broker runs here, and the view's denials do not depend on egress.
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	tctx := tools.ToolContext{}
	tctx.Source.WorkerBranchRoot = root
	tctx.Source.WorkerSourceRoots = []string{source}
	tctx.Source.WorkerViewBoundary = workspace.BranchViewBoundary(root)
	tctx.Local.LoopbackConnectGranted = true
	box, ok := confine.DefaultConfinement(hitl.ActionConfineRequest(tools.ActionConfineInputsForContext(tctx, nil)))
	if !ok {
		t.Fatal("a worker command got no boundary")
	}
	return box
}

// A worker's own git runs under its boundary, and its repository must stay reachable through the view.
func assertWorkerShellGitWorks(t *testing.T, root, source, worktree string) {
	t.Helper()
	box := workerCommandBox(t, root, source)
	git, err := osexec.LookPath("git")
	testutil.FailErr(t, "find git", err)
	plan := lexec.AgentLaunch(lexec.LaunchAgentCommand, "worker shell git", box).WithPathAuthority(confine.NativeLaunchPaths(worktree))
	out, code, err := lexec.Run(t.Context(), git, []string{"log", "--format=%s"}, lexec.ExecOpts{Launch: plan, Dir: worktree})
	if err != nil || code != 0 || !strings.Contains(string(out), "initial") {
		t.Fatalf("a worker's git under its boundary: code=%d err=%v out=%q", code, err, out)
	}
}

// assertWorkerProcessIsDenied builds the boundary a worker's own command gets, through the path its
// tool context takes, with the broadest loopback grant, and asks the kernel what it refuses.
func assertWorkerProcessIsDenied(t *testing.T, root, source string, view *adapter.PreparedView) {
	t.Helper()
	box := workerCommandBox(t, root, source)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "control listener", err)
	defer func() { _ = listener.Close() }()

	allowed := []denialprobe.Step{
		{Kind: denialprobe.ReadFile, Path: filepath.Join(root, "main.go")},
		{Kind: denialprobe.CreateFile, Path: filepath.Join(root, "worker-probe")},
		{Kind: denialprobe.Connect, Port: uint16(listener.Addr().(*net.TCPAddr).Port)}, //nolint:gosec // a listener's own port.
	}
	other := filepath.Join(filepath.Dir(root), "another-worker")
	testutil.FailErr(t, "mkdir another worker", os.MkdirAll(other, 0o700))
	otherFile := filepath.Join(other, "file")
	testutil.FailErr(t, "write another worker", os.WriteFile(otherFile, []byte("x"), 0o600))
	defer func() { _ = os.RemoveAll(other) }()
	backingEntry := viewconfine.FirstEntry(view.BackingDir)
	if backingEntry == "" {
		t.Fatal("a claimed view's backing store is empty, so nothing shows it is hidden")
	}
	denied := []denialprobe.Step{
		{Kind: denialprobe.Stat, Path: backingEntry},
		{Kind: denialprobe.ReadFile, Path: otherFile},
		{Kind: denialprobe.CreateFile, Path: filepath.Join(other, "worker-probe")},
		{Kind: denialprobe.CreateFile, Path: filepath.Join(view.BackingDir, "worker-probe")},
		{Kind: denialprobe.ReadFile, Path: filepath.Join(source, "main.go")},
		{Kind: denialprobe.CreateFile, Path: filepath.Join(source, "worker-probe")},
	}
	for _, port := range view.ListenerPorts {
		denied = append(denied, denialprobe.Step{Kind: denialprobe.Connect, Port: port})
	}
	denied = append(denied, denialprobe.Step{Kind: denialprobe.Unmount, Path: root})
	results, err := viewconfine.Launch(t.Context(), box, root, append(allowed, denied...))
	testutil.FailErr(t, "launch worker probe", err)
	for i, result := range results {
		if i < len(allowed) && !result.Succeeded() {
			t.Fatalf("a worker could not %s %s %d: errno %d", result.Kind, result.Path, result.Port, result.Errno)
		}
		if i >= len(allowed) && !result.Denied() {
			t.Fatalf("a worker was not refused %s %s %d: errno %d", result.Kind, result.Path, result.Port, result.Errno)
		}
	}
	if mounted, err := differentDevice(root); err != nil || !mounted {
		t.Fatalf("the view left its mount: mounted=%v err=%v", mounted, err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "main.go")); err != nil || string(raw) != "package main\n" {
		t.Fatalf("the kernel client stopped working after the denials: %q err=%v", raw, err)
	}
	for _, leftover := range []string{filepath.Join(source, "worker-probe"), filepath.Join(root, "worker-probe")} {
		if _, err := os.Lstat(leftover); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("probe left %s: %v", leftover, err)
		}
	}
}

func openFixtureRecovery(t *testing.T, branches string) *recovery.Coordinator {
	t.Helper()
	root := filepath.Join(branches, ".adapter", "recovery")
	store, err := checkpoint.NewStore(filepath.Join(root, "checkpoints"))
	testutil.FailErr(t, "open checkpoint store", err)
	coordinator, err := recovery.NewCoordinator(recovery.CoordinatorConfig{RootDir: root, CheckpointStore: store})
	testutil.FailErr(t, "open coordinator", err)
	return coordinator
}

func fixtureMountedSelector() *adapter.Selector {
	mounted := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: adapter.AdapterFUSELinux,
		MountProbeFn: func(context.Context, string, string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonNone, "", nil
		},
	})
	selector := adapter.NewSelector("linux", mounted, nil, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
	selector.RequireConfinement, selector.Confinement = true, fixtureConfinement{}
	return selector
}

func writeThroughClaimedView(t *testing.T, task *api.WorkerTask, rel, content string) {
	t.Helper()
	view, ok := workspace.LookupBranchClaimedView(task.WorkspaceRoot)
	if !ok {
		t.Fatalf("no claimed view for %s", task.ID)
	}
	path := layeredfs.QualifiedPath{RootID: view.Prepared.PrimaryRootID, Rel: rel}
	_, err := view.Prepared.Core.WriteFile(path, []byte(content), 0o644)
	testutil.FailErr(t, "write through view", err)
}

// Fixture evidence for the publication chain: no kernel mount. The store is shared by every worker,
// so each must find only its own checkpoint, and destroying one must leave the other retained.
func TestCompletionPublishesEachWorkerIntoTheRecoveryStoreAndDestroyDropsItsRetention(t *testing.T) {
	fixture := newLayeredWorkerFixture(t, fixtureMountedSelector())
	first := fixture.claim(t)
	second := fixture.claim(t)
	writeThroughClaimedView(t, first, "first.go", "package first\n")
	writeThroughClaimedView(t, second, "second.go", "package second\n")
	for _, task := range []*api.WorkerTask{first, second} {
		if won, err := fixture.queue.Complete(t.Context(), task, api.WorkerResult{Status: "complete"}); err != nil || !won {
			t.Fatalf("Complete %s: won=%v err=%v", task.ID, won, err)
		}
	}

	coordinator := openFixtureRecovery(t, fixture.branches)
	for task, want := range map[*api.WorkerTask]string{first: "first.go", second: "second.go"} {
		completed, ok := fixture.queue.Get(task.ID)
		if !ok || completed.MergeStatus != api.WorkerMergeStatusPending {
			t.Fatalf("worker %s is not pending review: %+v", task.ID, completed)
		}
		store, manifest, published, err := session.OpenTaskCheckpointStore(completed)
		testutil.FailErr(t, "open task checkpoint store", err)
		if !published || store.RootDir() != coordinator.CheckpointStore().RootDir() {
			t.Fatalf("worker %s published=%v store=%v", task.ID, published, store)
		}
		if manifest.WorkerID != task.ID || len(manifest.Entries) != 1 || manifest.Entries[0].Path.Rel != want {
			t.Fatalf("worker %s found another worker's checkpoint: %+v", task.ID, manifest)
		}
		journal, err := coordinator.LoadWorkerJournal(task.ID)
		testutil.FailErr(t, "load journal", err)
		if journal.LastPublishedCheckpointID != manifest.CheckpointID || journal.ActivePhase != "" {
			t.Fatalf("journal does not name the published checkpoint: %+v", journal)
		}
		if _, err := coordinator.VerifyCheckpointReconstructible(t.Context(), manifest.CheckpointID); err != nil {
			t.Fatalf("published checkpoint is not reconstructible: %v", err)
		}
	}

	testutil.FailErr(t, "destroy first", fixture.manager.DestroyWorkerWorkspace(&workspace.Binding{ID: first.ID, Root: first.WorkspaceRoot}))
	t.Cleanup(func() {
		_ = fixture.manager.DestroyWorkerWorkspace(&workspace.Binding{ID: second.ID, Root: second.WorkspaceRoot})
	})
	if _, err := coordinator.LoadWorkerJournal(first.ID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("destroy kept the journal: %v", err)
	}
	claims, err := coordinator.ListClaims()
	testutil.FailErr(t, "list claims", err)
	holders := map[string]bool{}
	for _, held := range claims {
		holders[held.WorkerID] = true
	}
	if holders[first.ID] || !holders[second.ID] {
		t.Fatalf("retention after destroying the first worker: %v", holders)
	}
	if ids, err := coordinator.ListMountIdentities(first.ID); err != nil || len(ids) != 0 {
		t.Fatalf("destroy kept the view identity: %v err=%v", ids, err)
	}
}

// Fixture evidence: the view has no kernel mount, but the process and its settlement are real.
func TestDestroySettlesARecordedWriterByItsIdentity(t *testing.T) {
	fixture := newLayeredWorkerFixture(t, fixtureMountedSelector())
	task := fixture.claim(t)
	writer := osexec.Command("/bin/sleep", "300")
	testutil.FailErr(t, "start writer", writer.Start())
	waited := make(chan error, 1)
	go func() { waited <- writer.Wait() }()
	t.Cleanup(func() { _ = writer.Process.Kill() })

	testutil.FailErr(t, "record writer", workspace.BranchProcessRecorder(task.WorkspaceRoot)(writer.Process.Pid))
	coordinator := openFixtureRecovery(t, fixture.branches)
	identity, ok, err := coordinator.LoadMountIdentity(task.ID, task.ID)
	testutil.FailErr(t, "load identity", err)
	if !ok || len(identity.RecordedProcesses) != 1 || identity.RecordedProcesses[0].PID != writer.Process.Pid {
		t.Fatalf("writer not recorded: ok=%v %+v", ok, identity)
	}

	testutil.FailErr(t, "destroy", fixture.manager.DestroyWorkerWorkspace(&workspace.Binding{ID: task.ID, Root: task.WorkspaceRoot}))
	<-waited
	if _, ok, err := coordinator.LoadMountIdentity(task.ID, task.ID); err != nil || ok {
		t.Fatalf("settled identity remains: ok=%v err=%v", ok, err)
	}
}

func TestRecordingARepeatedWriterDropsTheOnesThatEnded(t *testing.T) {
	fixture := newLayeredWorkerFixture(t, fixtureMountedSelector())
	task := fixture.claim(t)
	t.Cleanup(func() {
		_ = fixture.manager.DestroyWorkerWorkspace(&workspace.Binding{ID: task.ID, Root: task.WorkspaceRoot})
	})
	record := workspace.BranchProcessRecorder(task.WorkspaceRoot)
	for range 3 {
		ended := osexec.Command("/usr/bin/true")
		testutil.FailErr(t, "start", ended.Start())
		testutil.FailErr(t, "record", record(ended.Process.Pid))
		testutil.FailErr(t, "wait", ended.Wait())
	}
	identity, _, err := openFixtureRecovery(t, fixture.branches).LoadMountIdentity(task.ID, task.ID)
	testutil.FailErr(t, "load identity", err)
	if len(identity.RecordedProcesses) > 1 {
		t.Fatalf("ended writers accumulate: %+v", identity.RecordedProcesses)
	}
}

func TestAWorkerRepositoryIsPrivateAndLeavesTheCheckpoint(t *testing.T) {
	fixture := newLayeredWorkerFixture(t, fixtureMountedSelector())
	task := fixture.claim(t)
	view, ok := workspace.LookupBranchClaimedView(task.WorkspaceRoot)
	if !ok {
		t.Fatal("no claimed view")
	}
	core, rootID := view.Prepared.Core, view.Prepared.PrimaryRootID
	_, err := core.Mkdir(layeredfs.QualifiedPath{RootID: rootID, Rel: "nested"}, 0o755)
	testutil.FailErr(t, "mkdir nested", err)
	_, err = core.Mkdir(layeredfs.QualifiedPath{RootID: rootID, Rel: "nested/.git"}, 0o755)
	testutil.FailErr(t, "mkdir nested .git", err)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: rootID, Rel: "nested/.git/HEAD"}, []byte("ref: refs/heads/main\n"), 0o644)
	testutil.FailErr(t, "write nested HEAD", err)
	writeThroughClaimedView(t, task, "nested/file.go", "package nested\n")
	if won, err := fixture.queue.Complete(t.Context(), task, api.WorkerResult{Status: "complete"}); err != nil || !won {
		t.Fatalf("Complete: won=%v err=%v", won, err)
	}
	completed, _ := fixture.queue.Get(task.ID)
	_, manifest, published, err := session.OpenTaskCheckpointStore(completed)
	testutil.FailErr(t, "open checkpoint", err)
	captured := map[string]bool{}
	for _, entry := range manifest.Entries {
		captured[entry.Path.Rel] = true
		if layeredfs.IsGitMetadataPath(entry.Path.Rel) {
			t.Fatalf("checkpoint carries Git metadata: %s", entry.Path.Rel)
		}
	}
	if !published || !captured["nested/file.go"] {
		t.Fatalf("published=%v entries=%v", published, captured)
	}
	if _, err := os.Stat(filepath.Join(fixture.source, "nested")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("nested repository reached the source: %v", err)
	}
	testutil.FailErr(t, "destroy", fixture.manager.DestroyWorkerWorkspace(&workspace.Binding{ID: task.ID, Root: task.WorkspaceRoot}))
}
