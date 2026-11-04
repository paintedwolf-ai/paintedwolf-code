package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

type passingConfinement struct{}

func (passingConfinement) Available(context.Context, adapter.ProbeConfig) error    { return nil }
func (passingConfinement) VerifyView(context.Context, *adapter.PreparedView) error { return nil }

func materializedSource(t *testing.T) string {
	t.Helper()
	source, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve source", err)
	old := time.Now().Add(-time.Hour)
	for rel, content := range map[string]string{"main.go": "package main\n", "gone.go": "package gone\n", "kept/untouched.go": "package kept\n"} {
		path := filepath.Join(source, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write", os.WriteFile(path, []byte(content), 0o644))
		testutil.FailErr(t, "age", os.Chtimes(path, old, old))
	}
	return source
}

func openMaterializedRecovery(t *testing.T, branches string) *recovery.Coordinator {
	t.Helper()
	gitManager, err := OpenPrivateGit(branches)
	testutil.FailErr(t, "open private Git", err)
	t.Cleanup(func() { _ = gitManager.Store().Close() })
	recoveryDir := filepath.Join(branches, adapterRuntimeDirName, "recovery")
	store, err := checkpoint.NewStore(filepath.Join(recoveryDir, "checkpoints"))
	testutil.FailErr(t, "open store", err)
	coordinator, err := recovery.NewCoordinator(recovery.CoordinatorConfig{
		RootDir: recoveryDir, CheckpointStore: store, GitStore: gitManager.Store(), GitManager: gitManager,
		ProcessCtrl: recovery.RecordedProcessController{},
	})
	testutil.FailErr(t, "open recovery", err)
	return coordinator
}

func newMaterializedManager(t *testing.T, branches string) *Manager {
	t.Helper()
	coordinator := openMaterializedRecovery(t, branches)
	gitManager, err := OpenPrivateGit(branches)
	testutil.FailErr(t, "open private Git", err)
	t.Cleanup(func() { _ = gitManager.Store().Close() })
	return NewManager(branches).WithRecovery(coordinator).WithPrivateGit(gitManager)
}

func manifestByPath(t *testing.T, store *checkpoint.Store, checkpointID string) map[string]checkpoint.ManifestEntry {
	t.Helper()
	manifest, err := store.LoadManifest(checkpointID)
	testutil.FailErr(t, "load manifest", err)
	out := make(map[string]checkpoint.ManifestEntry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		out[entry.Path.Rel] = entry
	}
	return out
}

func TestCompletionCheckpointTakesBaseBytesFromThePristineTree(t *testing.T) {
	source := materializedSource(t)
	branches := t.TempDir()
	mgr := newMaterializedManager(t, branches)
	binding, err := mgr.CreateWorkerWorkspace(t.Context(), source, "job-base")
	testutil.FailErr(t, "create", err)
	if binding.Selection.Selection.Reason != adapter.SelectionUnderBudget {
		t.Fatalf("selection = %+v", binding.Selection.Selection)
	}
	if binding.View.Materialize.ContentBytesRead > binding.View.Materialize.CopiedBytes {
		t.Fatalf("preparation read more than it copied: %+v", binding.View.Materialize)
	}

	// The source moves on; the base stays what the worker started from.
	testutil.FailErr(t, "source moves", os.WriteFile(filepath.Join(source, "main.go"), []byte("package moved\n"), 0o644))
	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(binding.Root, "main.go"), []byte("package edited\n"), 0o644))
	testutil.FailErr(t, "delete", os.Remove(filepath.Join(binding.Root, "gone.go")))

	paths, handled, err := OverlayWorkspaceDivergencePaths(t.Context(), binding.Root)
	testutil.FailErr(t, "divergence", err)
	if !handled || strings.Join(paths, ",") != "gone.go,main.go" {
		t.Fatalf("divergence handled=%v paths=%v", handled, paths)
	}

	handled, err = PublishBranchCheckpoint(t.Context(), binding.Root)
	testutil.FailErr(t, "publish", err)
	if !handled {
		t.Fatal("a materialized branch must publish its own checkpoint")
	}
	storeDir, checkpointID, ok := BranchCheckpoint(binding.Root)
	if !ok {
		t.Fatal("the branch names no checkpoint")
	}
	store, err := checkpoint.NewStore(storeDir)
	testutil.FailErr(t, "open store", err)
	entries := manifestByPath(t, store, checkpointID)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	base, err := store.ReadBlob(entries["main.go"].Base.SHA256)
	testutil.FailErr(t, "read base blob", err)
	if string(base) != "package main\n" {
		t.Fatalf("base bytes = %q", base)
	}
	gone, err := store.ReadBlob(entries["gone.go"].Base.SHA256)
	testutil.FailErr(t, "read deleted base blob", err)
	if !entries["gone.go"].Deleted || string(gone) != "package gone\n" {
		t.Fatalf("deletion entry = %+v bytes=%q", entries["gone.go"], gone)
	}
	manifest, err := store.LoadManifest(checkpointID)
	testutil.FailErr(t, "load manifest", err)
	if manifest.Trigger != checkpoint.TriggerWorkerCompletion {
		t.Fatalf("trigger = %s", manifest.Trigger)
	}
	testutil.FailErr(t, "destroy", mgr.DestroyWorkerWorkspace(binding))
	if _, err := os.Stat(enginepaths.MetaDirForBranchRoot(binding.Root)); !os.IsNotExist(err) {
		t.Fatalf("destroy left the pristine tree: %v", err)
	}
}

func TestACopyThatPassesItsBudgetMovesOnToTheMount(t *testing.T) {
	source := materializedSource(t)
	var mounted atomic.Int32
	mount := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: adapter.AdapterFUSELinux,
		MountProbeFn: func(context.Context, string, string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonNone, "", nil
		},
		MountViewFn: func(context.Context, string, []adapter.PreparedRoot) error { mounted.Add(1); return nil },
	})
	selector := adapter.NewSelector("linux", mount, nil, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
	selector.RequireConfinement, selector.Confinement = true, passingConfinement{}
	// Clones cost nothing here, so the survey says under budget; then no clone succeeds.
	selector.BudgetFor = func(class adapter.BudgetClass) adapter.BudgetFact {
		return adapter.BudgetFact{Class: class, Limit: 30, EntryCost: 1}
	}
	branches := t.TempDir()
	mgr := newMaterializedManager(t, branches).WithSelector(selector)
	mgr.cloneAvailable = func(string) adapter.CloneAvailability {
		return func(context.Context, adapter.RootGrant) (bool, error) { return true, nil }
	}
	mgr.cloneFile = func(string, string) (bool, error) { return false, nil }

	binding, err := mgr.CreateWorkerWorkspace(t.Context(), source, "job-overrun")
	testutil.FailErr(t, "create", err)
	t.Cleanup(func() { _ = mgr.DestroyWorkerWorkspace(binding) })
	facts := binding.Selection.Selection
	if binding.Selection.Facts.PreparationKind != adapter.PreparationMounted || !facts.CopyPassedBudget || facts.Reason != adapter.SelectionOverBudgetMounted {
		t.Fatalf("selection = %+v", binding.Selection)
	}
	if mounted.Load() != 1 {
		t.Fatalf("mounted %d times", mounted.Load())
	}
	if _, err := os.Stat(enginepaths.PristineTreeForBranchRoot(binding.Root)); !os.IsNotExist(err) {
		t.Fatalf("the overrun left its pristine tree: %v", err)
	}
}

func TestTheCostIsDeclaredBeforeTheFirstCopy(t *testing.T) {
	source := materializedSource(t)
	denied := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: adapter.AdapterFUSELinux,
		MountProbeFn: func(context.Context, string, string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonMountDenied, "fixture", errors.New("EPERM")
		},
	})
	selector := adapter.NewSelector("linux", denied, nil, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
	selector.BudgetFor = adapter.ZeroBudget
	mgr := newMaterializedManager(t, t.TempDir()).WithSelector(selector)
	mgr.cloneAvailable = func(string) adapter.CloneAvailability {
		return func(context.Context, adapter.RootGrant) (bool, error) { return false, nil }
	}
	var declaredBeforeCopy atomic.Bool
	var declared atomic.Int64
	mgr.cloneFile = func(string, string) (bool, error) {
		declaredBeforeCopy.Store(declared.Load() > 0)
		return false, nil
	}
	ctx := WithPreparationReporter(t.Context(), func(p PreparationProgress) {
		if p.Stage == "snapshotting_branch" && p.TotalBytes > 0 {
			declared.CompareAndSwap(0, p.TotalBytes)
		}
	})
	binding, err := mgr.CreateWorkerWorkspace(ctx, source, "job-declared")
	testutil.FailErr(t, "create", err)
	t.Cleanup(func() { _ = mgr.DestroyWorkerWorkspace(binding) })
	if !declaredBeforeCopy.Load() {
		t.Fatal("the first copy started before the cost was declared")
	}
	wantBytes := int64(2 * (len("package main\n") + len("package gone\n") + len("package kept\n")))
	if declared.Load() != wantBytes {
		t.Fatalf("declared %d bytes, want %d", declared.Load(), wantBytes)
	}
	facts := binding.Selection.Selection
	if facts.Reason != adapter.SelectionOverBudgetMaterial || facts.MountUnavailable != adapter.ReasonMountDenied || facts.DeclaredCost.Partial {
		t.Fatalf("facts = %+v", facts)
	}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(binding.Root))
	testutil.FailErr(t, "load meta", err)
	if meta.SelectionReason != facts.Reason || meta.DeclaredCost != facts.DeclaredCost || meta.FallbackReason != adapter.ReasonMountDenied || meta.CopiedBytes != wantBytes {
		t.Fatalf("recorded facts = %+v", meta.JobMetaPreparation)
	}
}

func TestAWorkspaceTheDiskCannotHoldIsRefused(t *testing.T) {
	saved := queryAvailableStorageBytes
	t.Cleanup(func() { queryAvailableStorageBytes = saved })
	queryAvailableStorageBytes = func(string) (uint64, bool, error) { return 16, true, nil }
	mgr := NewManager(t.TempDir())
	_, err := mgr.CreateWorkerWorkspace(t.Context(), materializedSource(t), "job-full")
	var capacity *adapter.WorkspaceCapacityError
	if !errors.As(err, &capacity) || capacity.Available != 16 || !errors.Is(err, ErrWorkspaceStorageExhausted) {
		t.Fatalf("err = %v", err)
	}
}

func TestRestartReDerivesChangesAndRepositoryState(t *testing.T) {
	source := materializedSource(t)
	gittest.InitCommit(t, source, "initial")
	branches := t.TempDir()
	first := newMaterializedManager(t, branches)
	binding, err := first.CreateWorkerWorkspace(t.Context(), source, "job-restart")
	testutil.FailErr(t, "create", err)
	root := binding.Root
	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(root, "main.go"), []byte("package edited\n"), 0o644))
	gittest.Run(t, root, "add", "main.go")
	gittest.Run(t, root, "commit", "-m", "worker commit")
	commit := strings.TrimSpace(gittest.Run(t, root, "rev-parse", "HEAD"))

	// Nothing in memory crosses a restart.
	releaseBranchClaimState(root)
	sharedBranchViews.Delete(filepath.Clean(root))
	next := newMaterializedManager(t, branches)
	_, err = next.RecoverWorkerViews(t.Context())
	testutil.FailErr(t, "startup recovery", err)

	handled, err := PublishBranchCheckpoint(t.Context(), root)
	testutil.FailErr(t, "publish after restart", err)
	if !handled {
		t.Fatal("recovery did not offer the branch")
	}
	storeDir, checkpointID, ok := BranchCheckpoint(root)
	if !ok {
		t.Fatal("no checkpoint after restart")
	}
	store, err := checkpoint.NewStore(storeDir)
	testutil.FailErr(t, "open store", err)
	entries := manifestByPath(t, store, checkpointID)
	if len(entries) != 1 {
		t.Fatalf("recovery re-derived %+v", entries)
	}
	base, err := store.ReadBlob(entries["main.go"].Base.SHA256)
	testutil.FailErr(t, "read base", err)
	if string(base) != "package main\n" {
		t.Fatalf("base = %q", base)
	}
	manifest, err := store.LoadManifest(checkpointID)
	testutil.FailErr(t, "load manifest", err)
	found := false
	for _, oid := range manifest.PrivateGit.Refs {
		found = found || oid == commit
	}
	if !found {
		t.Fatalf("the worker's commit left the checkpoint: refs=%v want %s", manifest.PrivateGit.Refs, commit)
	}
	testutil.FailErr(t, "destroy", next.DestroyWorkerWorkspace(&Binding{ID: "job-restart", Root: root}))
}

func TestEvictedMaterializedBranchRebuildsFromItsCheckpoint(t *testing.T) {
	source := materializedSource(t)
	mgr := newMaterializedManager(t, t.TempDir())
	binding, err := mgr.CreateWorkerWorkspace(t.Context(), source, "job-evict")
	testutil.FailErr(t, "create", err)
	root := binding.Root
	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(root, "main.go"), []byte("package edited\n"), 0o644))

	// Eviction before any checkpoint would lose the only copy.
	if err := mgr.EvictWorkerBranch(t.Context(), root); !errors.Is(err, ErrBranchRetained) {
		t.Fatalf("unsealed eviction: %v", err)
	}
	if _, err := PublishBranchCheckpoint(t.Context(), root); err != nil {
		testutil.FailErr(t, "publish", err)
	}
	testutil.FailErr(t, "evict", mgr.EvictWorkerBranch(t.Context(), root))
	for _, gone := range []string{root, enginepaths.PristineTreeForBranchRoot(root), enginepaths.StatManifestForBranchRoot(root)} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s survived eviction: %v", gone, err)
		}
	}

	rebuilt, err := mgr.RebuildRecoveredBranch(t.Context(), root, "job-evict")
	testutil.FailErr(t, "rebuild", err)
	if !rebuilt {
		t.Fatal("no rebuild")
	}
	raw, err := os.ReadFile(filepath.Join(root, "main.go"))
	testutil.FailErr(t, "read rebuilt", err)
	if string(raw) != "package edited\n" {
		t.Fatalf("rebuilt = %q", raw)
	}
	if !adapter.HasStatManifest(enginepaths.StatManifestForBranchRoot(root)) {
		t.Fatal("the rebuilt branch has no manifest")
	}
	testutil.FailErr(t, "second edit", os.WriteFile(filepath.Join(root, "kept", "untouched.go"), []byte("package changed\n"), 0o644))
	handled, err := PublishBranchCheckpoint(t.Context(), root)
	testutil.FailErr(t, "publish rebuilt", err)
	if !handled {
		t.Fatal("the rebuilt view does not publish")
	}
	testutil.FailErr(t, "destroy", mgr.DestroyWorkerWorkspace(&Binding{ID: "job-evict", Root: root}))
}

func TestCanceledWorkKeepsOnlyWhatChanged(t *testing.T) {
	source := materializedSource(t)
	mgr := newMaterializedManager(t, t.TempDir())
	binding, err := mgr.CreateWorkerWorkspace(t.Context(), source, "job-cancel")
	testutil.FailErr(t, "create", err)
	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(binding.Root, "main.go"), []byte("package edited\n"), 0o644))
	testutil.FailErr(t, "delete", os.Remove(filepath.Join(binding.Root, "gone.go")))
	testutil.FailErr(t, "retain", mgr.RetainCanceledWork(t.Context(), "job-cancel", binding.Root, ""))
	records, err := mgr.recovery.ListInterruptedRecords("job-cancel")
	testutil.FailErr(t, "list", err)
	if len(records) != 1 || records[0].UnknownScope || len(records[0].AffectedPaths) != 2 {
		t.Fatalf("records = %+v", records)
	}
	raw, err := os.ReadFile(filepath.Join(records[0].QuarantineDir, "changed", "primary", "main.go"))
	testutil.FailErr(t, "read retained", err)
	if string(raw) != "package edited\n" {
		t.Fatalf("retained = %q", raw)
	}
	if _, err := os.Stat(filepath.Join(records[0].QuarantineDir, "changed", "primary", "kept", "untouched.go")); !os.IsNotExist(err) {
		t.Fatalf("an unchanged file was retained: %v", err)
	}
	testutil.FailErr(t, "destroy", mgr.DestroyWorkerWorkspace(binding))
}

func TestOneMaterializedViewServesEveryRoot(t *testing.T) {
	primary, secondary := materializedSource(t), materializedSource(t)
	roots := []projectroot.RootRef{
		{ID: "p", Label: "primary", Path: primary, IsPrimary: true},
		{ID: "s", Label: "secondary", Path: secondary},
	}
	mgr := newMaterializedManager(t, t.TempDir())
	binding, _, err := mgr.CreateWorkerWorkspaceFromSources(t.Context(), roots, roots, "p", "job-roots")
	testutil.FailErr(t, "create", err)
	if len(binding.View.Roots) != 2 {
		t.Fatalf("roots = %+v", binding.View.Roots)
	}
	dir, err := projectroot.BranchDirForID("s")
	testutil.FailErr(t, "branch dir", err)
	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(binding.Root, dir, "main.go"), []byte("package edited\n"), 0o644))
	handled, err := PublishBranchCheckpoint(t.Context(), binding.Root)
	testutil.FailErr(t, "publish", err)
	if !handled {
		t.Fatal("not handled")
	}
	storeDir, checkpointID, _ := BranchCheckpoint(binding.Root)
	store, err := checkpoint.NewStore(storeDir)
	testutil.FailErr(t, "open store", err)
	manifest, err := store.LoadManifest(checkpointID)
	testutil.FailErr(t, "load manifest", err)
	if len(manifest.Entries) != 1 || manifest.Entries[0].Path != (layeredfs.QualifiedPath{RootID: "s", Rel: "main.go"}) {
		t.Fatalf("entries = %+v", manifest.Entries)
	}
	testutil.FailErr(t, "destroy", mgr.DestroyWorkerWorkspace(binding))
}

func TestABranchWithoutAManifestKeepsItsBaselinePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj", "job")
	testutil.FailErr(t, "mkdir", os.MkdirAll(root, 0o755))
	testutil.FailErr(t, "meta", WriteJobMeta(enginepaths.MetaDirForBranchRoot(root), JobMeta{
		Roots: []JobMetaRoot{{ID: "primary", Path: "/project", IsPrimary: true}}, SnapshotComplete: true,
	}))
	handled, err := PublishBranchCheckpoint(t.Context(), root)
	if err != nil || handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if BranchUsesCheckpoints(root) {
		t.Fatal("a retained materialized branch keeps its baseline")
	}
	if _, handled, err := OverlayWorkspaceDivergencePaths(t.Context(), root); handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}

func TestIndexFactsCountACompletelyObservedRoot(t *testing.T) {
	source := materializedSource(t)
	project := "project-" + filepath.Base(source)
	root := sourcecatalog.Root{ID: "primary", Path: source}
	_, err := sourcecatalog.Process().Observe(t.Context(), project, []sourcecatalog.Root{root})
	testutil.FailErr(t, "observe", err)
	grants := withIndexFacts(t.Context(), project, []adapter.RootGrant{{RootID: "primary", SourcePath: source, IsPrimary: true}})
	if grants[0].Estimate == nil {
		t.Fatal("a settled catalog supplies no facts")
	}
	want := int64(len("package main\n") + len("package gone\n") + len("package kept\n"))
	if grants[0].Estimate.EstimatedBytes != want || grants[0].Estimate.EstimatedEntries < 3 {
		t.Fatalf("estimate = %+v", grants[0].Estimate)
	}
	if bare := withIndexFacts(t.Context(), "", []adapter.RootGrant{{RootID: "primary", SourcePath: source}}); bare[0].Estimate != nil {
		t.Fatalf("no project should mean no facts: %+v", bare[0].Estimate)
	}
}
