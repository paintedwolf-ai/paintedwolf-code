package worker

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func (f *layeredWorkerFixture) enqueueStacked(t *testing.T, baseID string) (string, error) {
	t.Helper()
	id, err := f.queue.Enqueue(t.Context(), api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: f.source, WorkspaceRootID: "root-id",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"main.go", "child.go"}, BaseOverlayID: baseID},
		AgentType: "implementer", Prompt: "Extend main.go", Brief: "Extend main.go",
	})
	testutil.FailErr(t, "enqueue stacked", err)
	if _, err := f.queue.ClaimNext(t.Context(), ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal}); err != nil {
		return id, err
	}
	return id, nil
}

func (f *layeredWorkerFixture) completeWithChange(t *testing.T, task *api.WorkerTask, write func()) *api.WorkerTask {
	t.Helper()
	write()
	if won, err := f.queue.Complete(t.Context(), task, api.WorkerResult{Status: "complete"}); err != nil || !won {
		t.Fatalf("Complete: won=%v err=%v", won, err)
	}
	completed, ok := f.queue.Get(task.ID)
	if !ok || completed.MergeStatus != api.WorkerMergeStatusPending {
		t.Fatalf("completed worker is not pending: %+v", completed)
	}
	return completed
}

func (f *layeredWorkerFixture) claimChild(t *testing.T, baseID string) *api.WorkerTask {
	t.Helper()
	id, err := f.enqueueStacked(t, baseID)
	testutil.FailErr(t, "claim next stacked", err)
	child, err := f.queue.ClaimWorkerBranch(t.Context(), id)
	testutil.FailErr(t, "claim stacked branch", err)
	next, ok := f.queue.Get(id)
	if !ok {
		t.Fatal("stacked job not found")
	}
	child.ClaimToken = next.ClaimToken
	return child
}

func assertFileHolds(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+filepath.Base(path), err)
	if string(raw) != want {
		t.Fatalf("%s = %q, want %q", filepath.Base(path), raw, want)
	}
}

// A materialized parent's published checkpoint is immutable even if its live tree changes.
func TestMaterializedStackedWorkerStartsFromItsParentsSealedResult(t *testing.T) {
	fixture := newLayeredWorkerFixture(t, nil)
	parent := fixture.claim(t)
	snap, _, ok := WorkerClaimWorkspaceState(parent.ID)
	if !ok || snap.Adapter.PreparationKind != adapter.PreparationMaterialized {
		t.Fatalf("expected a materialized parent: %+v", snap)
	}
	completed := fixture.completeWithChange(t, parent, func() {
		testutil.FailErr(t, "edit parent", os.WriteFile(filepath.Join(parent.WorkspaceRoot, "main.go"), []byte("package parent\n"), 0o644))
		testutil.FailErr(t, "remove in parent", os.Remove(filepath.Join(parent.WorkspaceRoot, "gone.go")))
	})
	_, parentManifest, published, err := session.OpenTaskCheckpointStore(completed)
	testutil.FailErr(t, "open parent checkpoint", err)
	if !published || parentManifest.WorkerID != parent.ID {
		t.Fatal("parent published no checkpoint")
	}
	// Nothing may read this: a later edit of the parent's tree must not reach the child.
	testutil.FailErr(t, "edit parent after seal", os.WriteFile(filepath.Join(parent.WorkspaceRoot, "main.go"), []byte("package later\n"), 0o644))

	child := fixture.claimChild(t, parent.ID)
	assertFileHolds(t, filepath.Join(child.WorkspaceRoot, "main.go"), "package parent\n")
	if _, err := os.Lstat(filepath.Join(child.WorkspaceRoot, "gone.go")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("parent's deletion did not reach the child: %v", err)
	}
	assertFileHolds(t, filepath.Join(fixture.source, "main.go"), "package main\n")
	coordinator, err := fixture.manager.CheckpointCoordinator()
	testutil.FailErr(t, "coordinator", err)
	claims, err := coordinator.ListClaims()
	testutil.FailErr(t, "claims", err)
	pinned := false
	for _, claim := range claims {
		pinned = pinned || (claim.Kind == recovery.ClaimStackedChild && claim.WorkerID == child.ID)
	}
	if !pinned {
		t.Fatalf("child holds no retention on its parent: %+v", claims)
	}
	childCompleted := fixture.completeWithChange(t, child, func() {
		testutil.FailErr(t, "edit child", os.WriteFile(filepath.Join(child.WorkspaceRoot, "main.go"), []byte("package child\n"), 0o644))
	})
	store, manifest, published, err := session.OpenTaskCheckpointStore(childCompleted)
	testutil.FailErr(t, "open child checkpoint", err)
	if !published {
		t.Fatal("child published no checkpoint")
	}
	foundBase := false
	for _, entry := range manifest.Entries {
		if entry.Path.Rel != "main.go" {
			continue
		}
		body, err := store.ReadBlob(entry.Base.SHA256)
		testutil.FailErr(t, "read child's retained merge base", err)
		if string(body) != "package parent\n" || entry.Base.Layer != layeredfs.LayerParent {
			t.Fatalf("child merge base = %q layer=%s", body, entry.Base.Layer)
		}
		foundBase = true
	}
	if !foundBase {
		t.Fatal("child checkpoint lost changed main.go")
	}
	testutil.FailErr(t, "destroy child", fixture.manager.DestroyWorkerWorkspace(&workspace.Binding{ID: child.ID, Root: child.WorkspaceRoot}))
	claims, err = coordinator.ListClaims()
	testutil.FailErr(t, "claims after destroy", err)
	for _, claim := range claims {
		if claim.Kind == recovery.ClaimStackedChild && claim.WorkerID == child.ID {
			t.Fatal("destroying the child kept its retention on the parent")
		}
	}
}

// Each sealed result is a delta over its own parent, so a grandchild sees the whole chain.
func TestMaterializedStackedChainReachesAGrandchild(t *testing.T) {
	fixture := newLayeredWorkerFixture(t, nil)
	parent := fixture.claim(t)
	fixture.completeWithChange(t, parent, func() {
		testutil.FailErr(t, "edit parent", os.WriteFile(filepath.Join(parent.WorkspaceRoot, "main.go"), []byte("package parent\n"), 0o644))
	})
	child := fixture.claimChild(t, parent.ID)
	fixture.completeWithChange(t, child, func() {
		testutil.FailErr(t, "add in child", os.WriteFile(filepath.Join(child.WorkspaceRoot, "child.go"), []byte("package child\n"), 0o644))
	})
	grandchild := fixture.claimChild(t, child.ID)
	assertFileHolds(t, filepath.Join(grandchild.WorkspaceRoot, "main.go"), "package parent\n")
	assertFileHolds(t, filepath.Join(grandchild.WorkspaceRoot, "child.go"), "package child\n")
}

func TestStackedWorkerRefusesAParentWithNoImmutableResult(t *testing.T) {
	fixture := newLayeredWorkerFixture(t, nil)
	running := fixture.claim(t)
	id, err := fixture.enqueueStacked(t, running.ID)
	testutil.FailErr(t, "claim next", err)
	if _, err := fixture.queue.ClaimWorkerBranch(t.Context(), id); !errors.Is(err, ErrWorkerBranchClaimFailed) {
		t.Fatalf("claim over a running parent = %v", err)
	}
	if task, ok := fixture.queue.Get(id); !ok || task.WorkspaceRoot != "" {
		t.Fatalf("refused claim left a branch: %+v", task)
	}
}

// stackedPromotionHook crashes a promotion after its effects and before its commit.
type stackedPromotionHook struct{ crash bool }

func (h *stackedPromotionHook) PrepareOverlayPromotion(context.Context, api.WorkerTask, []string, []string) (obligation.Plan, error) {
	if h.crash {
		panic(errPromoteCrash)
	}
	return obligation.Plan{}, nil
}

func (*stackedPromotionHook) PublishObligation(context.Context, obligation.Plan) error { return nil }

func testStackedWorkerPromotion(t *testing.T, selector *adapter.Selector, write func(*testing.T, *api.WorkerTask, string, string)) {
	fixture := newLayeredWorkerFixture(t, selector)
	parent := fixture.claim(t)
	if snap, _, ok := WorkerClaimWorkspaceState(parent.ID); !ok || snap.Adapter.PreparationKind != adapter.PreparationMounted {
		t.Skipf("this host cannot mount a view: %+v", snap.Adapter)
	}
	t.Cleanup(func() {
		_ = fixture.manager.DestroyWorkerWorkspace(&workspace.Binding{ID: parent.ID, Root: parent.WorkspaceRoot})
	})
	completed := fixture.completeWithChange(t, parent, func() { write(t, parent, "main.go", "package parent\n") })
	_, parentManifest, published, err := session.OpenTaskCheckpointStore(completed)
	testutil.FailErr(t, "open parent checkpoint", err)
	if !published {
		t.Fatal("parent published no checkpoint")
	}
	// The parent keeps its live view; a later edit must not reach the child.
	write(t, parent, "main.go", "package later\n")

	child := fixture.claimChild(t, parent.ID)
	t.Cleanup(func() {
		_ = fixture.manager.DestroyWorkerWorkspace(&workspace.Binding{ID: child.ID, Root: child.WorkspaceRoot})
	})
	view, ok := workspace.LookupBranchClaimedView(child.WorkspaceRoot)
	if !ok {
		t.Fatal("child has no claimed view")
	}
	seen, err := view.Prepared.Core.ReadFile(layeredfs.QualifiedPath{RootID: view.Prepared.PrimaryRootID, Rel: "main.go"})
	testutil.FailErr(t, "read through child view", err)
	if string(seen) != "package parent\n" {
		t.Fatalf("child view holds %q, not its parent's checkpoint", seen)
	}
	assertFileHolds(t, filepath.Join(fixture.source, "main.go"), "package main\n")

	childDone := fixture.completeWithChange(t, child, func() { write(t, child, "child.go", "package child\n") })
	_, childManifest, published, err := session.OpenTaskCheckpointStore(childDone)
	testutil.FailErr(t, "open child checkpoint", err)
	if !published || len(childManifest.ParentCheckpoints) == 0 || childManifest.ParentCheckpoints[0] != parentManifest.CheckpointID {
		t.Fatalf("child checkpoint parents = %v, want %s", childManifest.ParentCheckpoints, parentManifest.CheckpointID)
	}

	hook := &stackedPromotionHook{}
	dataDir := t.TempDir()
	svc := &MergeService{Queue: fixture.queue, Store: fixture.queue, DataDir: dataDir, Workspace: fixture.manager, Scans: hook}
	coordinator, err := fixture.manager.CheckpointCoordinator()
	testutil.FailErr(t, "coordinator", err)
	assertOriginalTarget := func(t *testing.T) {
		t.Helper()
		assertFileHolds(t, filepath.Join(fixture.source, "main.go"), "package main\n")
		if _, err := os.Lstat(filepath.Join(fixture.source, "child.go")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("failed promotion left child.go: %v", err)
		}
		for _, id := range []string{parentManifest.CheckpointID, childManifest.CheckpointID} {
			if _, landed, err := coordinator.LoadLandingReceipt(id); err != nil || landed {
				t.Fatalf("failed promotion left a landing for %s: landed=%v err=%v", id, landed, err)
			}
		}
	}

	// A failed commit restores the target before PromoteOverlay returns.
	_, err = fixture.db.ExecContext(t.Context(), `CREATE TRIGGER reject_stacked_completion BEFORE UPDATE OF merge_status ON worker_jobs WHEN NEW.merge_status='merged' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	testutil.FailErr(t, "install commit fault", err)
	if _, err := svc.PromoteOverlay(t.Context(), "", child.ID, api.PromoteOverlayInput{}); err == nil {
		t.Fatal("promotion ignored its commit failure")
	}
	assertOriginalTarget(t)
	if task, ok := fixture.queue.Get(child.ID); !ok || task.MergeStatus != api.WorkerMergeStatusPending {
		t.Fatalf("failed promotion left status %+v", task)
	}
	_, err = fixture.db.ExecContext(t.Context(), `DROP TRIGGER reject_stacked_completion`)
	testutil.FailErr(t, "remove commit fault", err)

	// A crash after every effect leaves a record that startup rolls back; nothing waits on a retry.
	hook.crash = true
	crashDuring(t, func() { _, _ = svc.PromoteOverlay(t.Context(), "", child.ID, api.PromoteOverlayInput{}) })
	assertFileHolds(t, filepath.Join(fixture.source, "child.go"), "package child\n")
	hook.crash = false
	_, err = fixture.db.ExecContext(t.Context(), `UPDATE worker_jobs SET merge_lease_expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, child.ID)
	testutil.FailErr(t, "expire crashed lease", err)
	restarted := &MergeService{Queue: fixture.queue, Store: fixture.queue, DataDir: dataDir, Workspace: fixture.manager, Scans: hook}
	testutil.FailErr(t, "startup recovery", restarted.RecoverOrphanedMergeApplies(t.Context()))
	assertOriginalTarget(t)

	result, err := restarted.PromoteOverlay(t.Context(), "", child.ID, api.PromoteOverlayInput{})
	testutil.FailErr(t, "promote after recovery", err)
	if result.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("status = %s", result.Status)
	}
	assertFileHolds(t, filepath.Join(fixture.source, "main.go"), "package parent\n")
	assertFileHolds(t, filepath.Join(fixture.source, "child.go"), "package child\n")
	for _, id := range []string{parentManifest.CheckpointID, childManifest.CheckpointID} {
		if _, landed, err := coordinator.LoadLandingReceipt(id); err != nil || !landed {
			t.Fatalf("no landing for %s: landed=%v err=%v", id, landed, err)
		}
	}
	claims, err := coordinator.ListClaims()
	testutil.FailErr(t, "claims", err)
	for _, claim := range claims {
		if claim.Kind == recovery.ClaimPromotion {
			t.Fatalf("settled promotion kept its claim: %+v", claim)
		}
	}
}

func TestStackedWorkerClaimsAndPromotesThroughTheOneTransaction(t *testing.T) {
	writeCore := func(t *testing.T, task *api.WorkerTask, rel, content string) {
		writeThroughClaimedView(t, task, rel, content)
	}
	t.Run("fixture view", func(t *testing.T) { testStackedWorkerPromotion(t, fixtureMountedSelector(), writeCore) })
	t.Run("real mount", func(t *testing.T) {
		selector := platform.NewSelector(platform.Config{Confinement: kernelMountCheck{}})
		testStackedWorkerPromotion(t, selector, func(t *testing.T, task *api.WorkerTask, rel, content string) {
			testutil.FailErr(t, "write through the mount", os.WriteFile(filepath.Join(task.WorkspaceRoot, rel), []byte(content), 0o644))
		})
	})
}

// A worker's commit in a non-primary root reaches that root's repository at promotion, and rollback
// takes it back out. Real mounts: this is the path a worker's own Git takes.
func TestNonPrimaryRootRefsReachPromotionAndRollBack(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve base", err)
	roots := make([]projectroot.RootRef, 0, 2)
	for _, id := range []string{"p", "s"} {
		source := filepath.Join(base, "source-"+id)
		testutil.FailErr(t, "mkdir source", os.MkdirAll(source, 0o755))
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(source, "file.txt"), []byte(id+"\n"), 0o644))
		gittest.InitCommit(t, source, "initial "+id)
		roots = append(roots, projectroot.RootRef{ID: id, Label: id, Path: source, IsPrimary: id == "p"})
	}
	privateGit, err := workspace.OpenPrivateGit(filepath.Join(base, "branches"))
	testutil.FailErr(t, "open private Git", err)
	t.Cleanup(func() { _ = privateGit.Store().Close() })
	selector := platform.NewSelector(platform.Config{Confinement: kernelMountCheck{}})
	manager := workspace.NewManager(filepath.Join(base, "branches"), filepath.Join(base, "seeds")).WithPrivateGit(privateGit).WithSelector(selector)
	binding, _, err := manager.CreateWorkerBranch(workspace.WithWorkspaceProject(t.Context(), testdbseed.DefaultProjectID), roots, "p", "job-roots")
	testutil.FailErr(t, "create branch", err)
	t.Cleanup(func() { _ = manager.DestroyWorkerWorkspace(binding) })
	if binding.Selection.Facts.PreparationKind != adapter.PreparationMounted {
		t.Skipf("this host cannot mount a view: %s", binding.Selection.Facts.FallbackReason)
	}
	dir, err := projectroot.BranchDirForID("s")
	testutil.FailErr(t, "branch dir", err)
	secondary := filepath.Join(binding.Root, dir)
	worktree, finish, managed, err := privategit.PrepareManagerViewOperation(t.Context(), privateGit, secondary, binding.ID)
	testutil.FailErr(t, "prepare secondary Git", err)
	if !managed {
		t.Fatal("secondary root is not a managed view")
	}
	testutil.FailErr(t, "finish prepare", finish())
	testutil.FailErr(t, "write in secondary", os.WriteFile(filepath.Join(worktree, "added.txt"), []byte("added\n"), 0o644))
	gitExec := privategit.NewGitExecutor(privateGit, privategit.SecondaryViewID(binding.ID, "s"), privategit.RemotePolicy{})
	_, err = gitExec.Run(t.Context(), ".", "add", "added.txt")
	testutil.FailErr(t, "add", err)
	_, err = gitExec.Commit(t.Context(), "worker commit")
	testutil.FailErr(t, "commit", err)
	out, err := gitExec.Run(t.Context(), ".", "rev-parse", "HEAD")
	testutil.FailErr(t, "rev-parse", err)
	commit := strings.TrimSpace(out)
	gitDir := filepath.Join(roots[1].Path, ".git")
	if _, statErr := os.Stat(filepath.Join(gitDir, "objects", commit[:2], commit[2:])); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("private commit reached the source repository early: %v", statErr)
	}

	handled, err := workspace.PublishBranchCheckpoint(t.Context(), binding.Root)
	testutil.FailErr(t, "publish", err)
	if !handled {
		t.Fatal("mounted branch was not published")
	}
	task := &api.WorkerTask{ID: binding.ID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: roots[0].Path, WorkspaceRoot: binding.Root}
	_, manifest, published, err := session.OpenTaskCheckpointStore(task)
	testutil.FailErr(t, "open checkpoint", err)
	if !published {
		t.Fatal("no checkpoint published")
	}
	found := false
	for _, oid := range manifest.PrivateGit.Roots["s"].Refs {
		found = found || oid == commit
	}
	if !found {
		t.Fatalf("manifest omits the secondary root's commit %s: %+v", commit, manifest.PrivateGit.Roots)
	}

	txn, err := checkpoint.BeginTransaction(checkpoint.TransactionConfig{Dir: filepath.Join(base, "tx"), ID: "roots"})
	testutil.FailErr(t, "begin", err)
	testutil.FailErr(t, "apply", session.ApplyTaskCheckpoint(t.Context(), txn, task, roots))
	imported := gittest.Run(t, roots[1].Path, "for-each-ref", "--format=%(objectname)", "refs/lycaon/workers/"+binding.ID)
	if !strings.Contains(imported, commit) {
		t.Fatalf("secondary ref not imported: %q", imported)
	}
	if head := gittest.Run(t, roots[1].Path, "rev-parse", "HEAD"); head == commit {
		t.Fatal("import moved the source repository's HEAD")
	}
	testutil.FailErr(t, "rollback", txn.Rollback(t.Context()))
	if refs := gittest.Run(t, roots[1].Path, "for-each-ref", "refs/lycaon"); refs != "" {
		t.Fatalf("rollback left refs in the secondary root: %q", refs)
	}
}
