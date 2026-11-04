package worker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// fixtureConfinement stands in for enforcement around the fixture MountedAdapter. It proves nothing
// about denial; those tests exercise claim sequencing, not confinement.
type fixtureConfinement struct{}

func (fixtureConfinement) Available(context.Context, adapter.ProbeConfig) error    { return nil }
func (fixtureConfinement) VerifyView(context.Context, *adapter.PreparedView) error { return nil }

// kernelMountCheck is the check supplied until real confinement lands. It proves a kernel mount sits
// on each root and the backing store is private to this user. That is fixture evidence: no sandboxed
// process is denied anything here, which is what real enforcement must add.
type kernelMountCheck struct{}

func (kernelMountCheck) Available(context.Context, adapter.ProbeConfig) error { return nil }

func (kernelMountCheck) VerifyView(_ context.Context, view *adapter.PreparedView) error {
	for _, root := range view.Roots {
		mounted, err := differentDevice(root.WorkspacePath)
		if err != nil {
			return err
		}
		if !mounted {
			return fmt.Errorf("root %s is not a kernel mount", root.RootID)
		}
	}
	info, err := os.Stat(view.BackingDir)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("backing dir mode %o is not private", info.Mode().Perm())
	}
	return nil
}

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

// Runs on a real kernel mount with the host's real adapter and services. kernelMountCheck is the one
// fixture: it stands in for the enforcement a later lane supplies.
func TestOrdinaryWorkerClaimThroughARealMountedViewToCompletion(t *testing.T) {
	selector := platform.NewSelector(platform.Config{Confinement: kernelMountCheck{}})
	fixture := newLayeredWorkerFixture(t, selector)
	task := fixture.claim(t)
	snap, _, ok := WorkerClaimWorkspaceState(task.ID)
	if !ok {
		t.Fatal("claim recorded no workspace state")
	}
	if snap.Adapter.PreparationKind != adapter.PreparationMounted {
		t.Skipf("this host cannot mount a view: %s", snap.Adapter.FallbackReason)
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
