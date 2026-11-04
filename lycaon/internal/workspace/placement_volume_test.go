//go:build darwin || linux

package workspace

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testvolume"
	"github.com/lycaon/lycaon/internal/workerplacement"
)

type placementFixture struct {
	state string
	mgr   *Manager
}

func newPlacementFixture(t *testing.T) placementFixture {
	t.Helper()
	state, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve state", err)
	return placementFixtureAt(t, state)
}

func placementFixtureAt(t *testing.T, state string) placementFixture {
	t.Helper()
	testutil.FailErr(t, "create state", os.MkdirAll(state, 0o700))
	t.Cleanup(func() {
		if _, err := workerplacement.Purge(context.WithoutCancel(t.Context()), state); err != nil {
			t.Errorf("purge placements: %v", err)
		}
	})
	return placementFixture{
		state: state,
		mgr:   NewManager(enginepaths.WorkerBranchesRootUnder(state), enginepaths.WorkerSeedsRootUnder(state)),
	}
}

func writeTree(t *testing.T, root string, files map[string]string) int64 {
	t.Helper()
	var total int64
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write", os.WriteFile(path, []byte(body), 0o644))
		total += int64(len(body))
	}
	return total
}

var projectFiles = map[string]string{
	"main.go":        strings.Repeat("package main\n", 400),
	"pkg/a.go":       strings.Repeat("package pkg\n", 900),
	"pkg/sub/b.txt":  "b",
	".hidden/config": "c",
}

// OS-managed entries on a volume root change on their own, independent of clones.
var osManagedRootEntries = map[string]bool{
	".fseventsd": true, ".Spotlight-V100": true, ".Trashes": true, ".TemporaryItems": true, "lost+found": true,
}

func entryCount(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				return filepath.SkipDir
			}
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.IsDir() && filepath.Dir(path) == root && osManagedRootEntries[entry.Name()] {
			return filepath.SkipDir
		}
		count++
		return nil
	})
	testutil.FailErr(t, "count entries", err)
	return count
}

func assertOutside(t *testing.T, branch string, roots ...string) {
	t.Helper()
	if err := workerplacement.AssertOutsideRoots(branch, roots); err != nil {
		t.Fatalf("branch is inside an attached root: %v", err)
	}
}

func volumeListing(t *testing.T, root string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(root)
	testutil.FailErr(t, "list volume", err)
	out := map[string]bool{}
	for _, entry := range entries {
		if !osManagedRootEntries[entry.Name()] {
			out[entry.Name()] = true
		}
	}
	return out
}

func TestSecondVolumeSourceClonesWithZeroCopiedBytes(t *testing.T) {
	volume := testvolume.New(t, testvolume.Clonable)
	project := volume.Dir(t, "project")
	want := writeTree(t, project, projectFiles)
	fixture := newPlacementFixture(t)

	first, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), project, "job-1")
	testutil.FailErr(t, "first clone", err)
	second, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), project, "job-2")
	testutil.FailErr(t, "second clone", err)

	for _, binding := range []*Binding{first, second} {
		if !binding.Preparation.Clonable || binding.Preparation.NotClonableReason != "" {
			t.Fatalf("preparation = %+v", binding.Preparation)
		}
		prepared := binding.Preparation.Roots[0]
		if prepared.CopiedBytes != 0 || prepared.ClonedBytes != want || prepared.ClonedFiles != int64(len(projectFiles)) {
			t.Fatalf("not a clone: %+v, want %d cloned bytes", prepared, want)
		}
		if prepared.Strategy != ProvisionDirectCoW {
			t.Fatalf("strategy = %s", prepared.Strategy)
		}
		if !enginepaths.IsWorkerBranchPath(binding.Root) {
			t.Fatalf("placed branch lost the path segment: %s", binding.Root)
		}
		assertOutside(t, binding.Root, project)
	}
	if got, source := entryCount(t, first.Root), entryCount(t, project); got != source {
		t.Fatalf("first clone has %d entries, source %d", got, source)
	}
	if first, second := entryCount(t, first.Root), entryCount(t, second.Root); first != second {
		t.Fatalf("clone entry counts differ: %d, %d", first, second)
	}
	logical, ok, err := workerplacement.LogicalFromPhysical(fixture.state, first.Root)
	testutil.FailErr(t, "logical address", err)
	if !ok || !strings.HasPrefix(logical, enginepaths.WorkerBranchesDirName+"/") {
		t.Fatalf("logical address = %q, %v", logical, ok)
	}
	physical, err := workerplacement.PhysicalFromLogical(fixture.state, logical)
	testutil.FailErr(t, "physical address", err)
	if physical != first.Root {
		t.Fatalf("physical = %s, want %s", physical, first.Root)
	}
}

func TestRootAttachedAtVolumeRootCopiesToStateFolderAndNeverOntoTheVolume(t *testing.T) {
	volume := testvolume.New(t, testvolume.NoClone)
	writeTree(t, volume.Root, projectFiles)
	before := volumeListing(t, volume.Root)
	fixture := newPlacementFixture(t)

	first, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), volume.Root, "job-1")
	testutil.FailErr(t, "first", err)
	second, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), volume.Root, "job-2")
	testutil.FailErr(t, "second", err)

	if first.Preparation.Clonable || first.Preparation.NotClonableReason != workerplacement.ReasonVolumeRootInRoot {
		t.Fatalf("preparation = %+v", first.Preparation)
	}
	assertOutside(t, first.Root, volume.Root)
	if !strings.HasPrefix(first.Root, enginepaths.WorkerBranchesRootUnder(fixture.state)) {
		t.Fatalf("branch left the state folder: %s", first.Root)
	}
	after := volumeListing(t, volume.Root)
	if len(after) != len(before) {
		t.Fatalf("volume changed: before %v, after %v", before, after)
	}
	if a, b := entryCount(t, first.Root), entryCount(t, second.Root); a != b {
		t.Fatalf("entry counts: first %d, second %d", a, b)
	}
	for rel := range projectFiles {
		if _, err := os.Lstat(filepath.Join(first.Root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("clone is missing %s: %v", rel, err)
		}
	}
}

func TestHomeAttachmentHoldingTheStateFolderClonesTwiceWithoutGrowth(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve home", err)
	home := filepath.Join(base, "home")
	writeTree(t, home, projectFiles)
	source := entryCount(t, home)
	fixture := placementFixtureAt(t, filepath.Join(home, ".config", "paintedwolf"))
	testutil.FailErr(t, "write state file", os.WriteFile(filepath.Join(fixture.state, "store.db"), []byte("state"), 0o600))

	first, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), home, "job-1")
	testutil.FailErr(t, "first", err)
	second, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), home, "job-2")
	testutil.FailErr(t, "second", err)
	assertOutside(t, first.Root, home)
	assertOutside(t, second.Root, home)
	if a, b := entryCount(t, first.Root), entryCount(t, second.Root); a != b {
		t.Fatalf("second clone has %d entries, first %d", b, a)
	}
	// The .config parent is kept; the state folder and every branch in it are not.
	if got := entryCount(t, first.Root); got != source+1 {
		t.Fatalf("clone has %d entries, want %d", got, source+1)
	}
	if _, err := os.Lstat(filepath.Join(first.Root, ".config", "paintedwolf")); !os.IsNotExist(err) {
		t.Fatalf("the state folder was copied into a worker tree: %v", err)
	}
}

func TestHomeOnSecondVolumePlacesAtVolumeRoot(t *testing.T) {
	volume := testvolume.New(t, testvolume.Clonable)
	home := volume.Dir(t, "home")
	writeTree(t, home, projectFiles)
	fixture := placementFixtureAt(t, filepath.Join(home, ".config", "paintedwolf"))

	first, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), home, "job-1")
	testutil.FailErr(t, "first", err)
	second, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), home, "job-2")
	testutil.FailErr(t, "second", err)
	assertOutside(t, first.Root, home)
	if first.Preparation.PlacementFolder == "" || first.Preparation.Roots[0].CopiedBytes != 0 {
		t.Fatalf("preparation = %+v", first.Preparation)
	}
	if a, b := entryCount(t, first.Root), entryCount(t, second.Root); a != b {
		t.Fatalf("second clone has %d entries, first %d", b, a)
	}
}

func TestRootsSpanningVolumesAreNotClonable(t *testing.T) {
	volume := testvolume.New(t, testvolume.NoClone)
	onVolume := volume.Dir(t, "one")
	other := t.TempDir()
	writeTree(t, onVolume, map[string]string{"a.txt": "a"})
	writeTree(t, other, map[string]string{"b.txt": "b"})
	fixture := newPlacementFixture(t)
	roots := []projectroot.RootRef{
		{ID: "primary", Path: onVolume, Label: "one", IsPrimary: true},
		{ID: "second", Path: other, Label: "two"},
	}
	binding, _, err := fixture.mgr.CreateWorkerWorkspaceFromSources(t.Context(), roots, roots, "primary", "job")
	testutil.FailErr(t, "create", err)
	if binding.Preparation.Clonable || binding.Preparation.NotClonableReason != workerplacement.ReasonRootsSpanVolumes {
		t.Fatalf("preparation = %+v", binding.Preparation)
	}
	if binding.Preparation.PlacementFolder != "" {
		t.Fatalf("placement folder = %s", binding.Preparation.PlacementFolder)
	}
}

func TestUnwritableVolumeRootIsNotClonableAndWritesNothing(t *testing.T) {
	volume := testvolume.New(t, testvolume.Movable)
	project := volume.Dir(t, "project")
	writeTree(t, project, projectFiles)
	volume.MakeReadOnly(t)
	before := volumeListing(t, volume.Root)
	fixture := newPlacementFixture(t)

	binding, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), project, "job")
	testutil.FailErr(t, "create", err)
	if binding.Preparation.Clonable || binding.Preparation.NotClonableReason != workerplacement.ReasonVolumeRootReadOnly {
		t.Fatalf("preparation = %+v", binding.Preparation)
	}
	after := volumeListing(t, volume.Root)
	if len(after) != len(before) || !after["project"] {
		t.Fatalf("volume changed: before %v, after %v", before, after)
	}
	registry, err := workerplacement.Load(fixture.state)
	testutil.FailErr(t, "load registry", err)
	if len(registry.Folders) != 0 {
		t.Fatalf("failed reservation stayed registered: %+v", registry.Folders)
	}
}

func TestSourceThatCannotCloneDeclaresCostBeforeCopying(t *testing.T) {
	volume := testvolume.New(t, testvolume.NoClone)
	project := volume.Dir(t, "project")
	want := writeTree(t, project, projectFiles)
	fixture := newPlacementFixture(t)
	fixture.mgr.seedRoot = ""
	wantEntries := int64(entryCount(t, project))

	var declared atomic.Pointer[PreparationProgress]
	var filesAtDeclaration atomic.Int64
	var branchAtDeclaration atomic.Int64
	branch := fixture.mgr.jobRoot(project, "job")
	ctx := WithPreparationReporter(t.Context(), func(progress PreparationProgress) {
		if progress.Stage != StageCostDeclared {
			return
		}
		copied := progress
		declared.Store(&copied)
		filesAtDeclaration.Store(progress.Files)
		branchAtDeclaration.Store(int64(entryCount(t, branch)))
	})
	binding, err := fixture.mgr.CreateWorkerWorkspace(ctx, project, "job")
	testutil.FailErr(t, "create", err)

	fact := declared.Load()
	if fact == nil {
		t.Fatal("no cost was declared")
	}
	if fact.TotalEntries != wantEntries || fact.TotalBytes != want {
		t.Fatalf("declared %d entries and %d bytes, want %d and %d", fact.TotalEntries, fact.TotalBytes, wantEntries, want)
	}
	if fact.NotClonableReason == "" {
		t.Fatalf("declared cost has no reason: %+v", fact)
	}
	if filesAtDeclaration.Load() != 0 || branchAtDeclaration.Load() != 0 {
		t.Fatalf("copying began before the declaration: %d files, %d entries", filesAtDeclaration.Load(), branchAtDeclaration.Load())
	}
	prepared := binding.Preparation.Roots[0]
	if prepared.Strategy != ProvisionDirectCopy || prepared.CopiedBytes != want || prepared.DeclaredBytes != want || prepared.DeclaredEntries != wantEntries {
		t.Fatalf("prepared = %+v", prepared)
	}
	if _, err := os.Lstat(filepath.Join(volume.Root, enginepaths.WorkerPlacementDirName(os.Getuid(), fixture.state))); !os.IsNotExist(err) {
		t.Fatalf("placement folder left on a volume that cannot clone: %v", err)
	}
}

func TestDiskThatCannotHoldTheCopyRefusesWithTheNumbers(t *testing.T) {
	volume := testvolume.New(t, testvolume.NoClone)
	project := volume.Dir(t, "project")
	want := writeTree(t, project, projectFiles)
	fixture := newPlacementFixture(t)
	fixture.mgr.seedRoot = ""
	previous := queryAvailableStorageBytes
	queryAvailableStorageBytes = func(string) (uint64, bool, error) { return 16, true, nil }
	t.Cleanup(func() { queryAvailableStorageBytes = previous })

	_, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), project, "job")
	var capacity *CapacityError
	if !errors.As(err, &capacity) || !errors.Is(err, ErrWorkspaceStorageExhausted) {
		t.Fatalf("err = %v", err)
	}
	if capacity.Required != uint64(want) || capacity.Available != 16 {
		t.Fatalf("capacity = %+v", capacity)
	}
	if _, statErr := os.Lstat(fixture.mgr.jobRoot(project, "job")); !os.IsNotExist(statErr) {
		t.Fatalf("refused preparation left a tree: %v", statErr)
	}
}

func TestCancelledCopyReleasesOnlyItsOwnPreparation(t *testing.T) {
	volume := testvolume.New(t, testvolume.NoClone)
	project := volume.Dir(t, "project")
	writeTree(t, project, projectFiles)
	fixture := newPlacementFixture(t)
	fixture.mgr.seedRoot = ""
	kept, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), project, "kept")
	testutil.FailErr(t, "create kept", err)
	keptEntries := entryCount(t, kept.Root)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = WithPreparationReporter(ctx, func(progress PreparationProgress) {
		if progress.Stage == StageCostDeclared {
			cancel()
		}
	})
	_, err = fixture.mgr.CreateWorkerWorkspace(ctx, project, "canceled")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	canceled := fixture.mgr.jobRoot(project, "canceled")
	for _, path := range []string{canceled, enginepaths.MetaDirForBranchRoot(canceled)} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("canceled preparation left %s: %v", path, statErr)
		}
	}
	if got := entryCount(t, kept.Root); got != keptEntries {
		t.Fatalf("another worker's tree changed: %d, want %d", got, keptEntries)
	}
	if _, err := LoadBranchLayout(kept.Root); err != nil {
		t.Fatalf("another worker's branch lost its layout: %v", err)
	}
}

func TestEjectedVolumeKeepsRecordsAndTheWorkerRecoversOnRemount(t *testing.T) {
	volume := testvolume.New(t, testvolume.Movable)
	t.Cleanup(UseCopyAsCloneForTest())
	project := volume.Dir(t, "project")
	writeTree(t, project, projectFiles)
	fixture := newPlacementFixture(t)
	binding, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), project, "job")
	testutil.FailErr(t, "create", err)
	if binding.Preparation.PlacementFolder == "" {
		t.Fatalf("not placed: %+v", binding.Preparation)
	}
	edit := filepath.Join(binding.Root, "pkg", "worker-edit.txt")
	testutil.FailErr(t, "worker edit", os.WriteFile(edit, []byte("kept"), 0o644))

	volume.Eject(t)

	trees, err := ListJobTrees(t.Context(), fixture.mgr.branchRoot)
	testutil.FailErr(t, "inventory while ejected", err)
	if len(trees) != 1 || !trees[0].Unavailable || trees[0].Present || trees[0].Root != binding.Root {
		t.Fatalf("trees = %+v", trees)
	}
	if err := fixture.mgr.DestroyWorkerWorkspace(binding); !errors.Is(err, ErrBranchVolumeUnavailable) {
		t.Fatalf("destroy err = %v", err)
	}
	if _, err := AcquireBranchUse(t.Context(), binding.Root); !errors.Is(err, ErrBranchVolumeUnavailable) {
		t.Fatalf("acquire err = %v", err)
	}
	if err := EvictJobTree(t.Context(), binding.Root); !errors.Is(err, ErrBranchVolumeUnavailable) {
		t.Fatalf("evict err = %v", err)
	}
	TouchBranchUse(binding.Root)
	if _, _, err := fixture.mgr.CreateWorkerWorkspaceFromSources(t.Context(),
		[]projectroot.RootRef{{ID: "primary", Path: project, IsPrimary: true}},
		[]projectroot.RootRef{{ID: "primary", Path: project, IsPrimary: true}}, "primary", "job"); !errors.Is(err, ErrBranchVolumeUnavailable) {
		t.Fatalf("reclaim err = %v", err)
	}
	removed, err := ReconcileStaleSandboxes(t.Context(), fixture.mgr.branchRoot, project, func(context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{}, nil
	})
	testutil.FailErr(t, "reconcile while ejected", err)
	if removed != 0 {
		t.Fatalf("reconciliation removed %d entries for an absent volume", removed)
	}
	if entries, err := os.ReadDir(volume.Root); err != nil || len(entries) != 0 {
		t.Fatalf("the bare mount point changed: %v, %v", entries, err)
	}

	volume.Remount(t)

	if raw, err := os.ReadFile(edit); err != nil || string(raw) != "kept" {
		t.Fatalf("worker edit after remount: %q, %v", raw, err)
	}
	if _, err := LoadBranchLayout(binding.Root); err != nil {
		t.Fatalf("layout after remount: %v", err)
	}
	release, err := AcquireBranchUse(t.Context(), binding.Root)
	testutil.FailErr(t, "lease after remount", err)
	release()
	trees, err = ListJobTrees(t.Context(), fixture.mgr.branchRoot)
	testutil.FailErr(t, "inventory after remount", err)
	if len(trees) != 1 || trees[0].Unavailable || !trees[0].Present || trees[0].Root != binding.Root {
		t.Fatalf("trees after remount = %+v", trees)
	}
	testutil.FailErr(t, "destroy after remount", fixture.mgr.DestroyWorkerWorkspace(binding))
	registry, err := workerplacement.Load(fixture.state)
	testutil.FailErr(t, "registry", err)
	if len(registry.Folders) != 0 || len(registry.Branches) != 0 {
		t.Fatalf("registry after the last branch: %+v", registry)
	}
}

func TestPlacedBranchIsFoundByRetentionAndLifecycle(t *testing.T) {
	volume := testvolume.New(t, testvolume.Movable)
	t.Cleanup(UseCopyAsCloneForTest())
	project := volume.Dir(t, "project")
	writeTree(t, project, projectFiles)
	fixture := newPlacementFixture(t)
	binding, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), project, "job")
	testutil.FailErr(t, "create", err)

	trees, err := ListJobTrees(t.Context(), fixture.mgr.branchRoot)
	testutil.FailErr(t, "inventory", err)
	if len(trees) != 1 || trees[0].Root != binding.Root || !trees[0].Present || trees[0].AllocatedBytes <= 0 {
		t.Fatalf("trees = %+v", trees)
	}
	testutil.FailErr(t, "evict", EvictJobTree(t.Context(), binding.Root))
	if _, err := os.Lstat(binding.Root); !os.IsNotExist(err) {
		t.Fatalf("evicted tree remains: %v", err)
	}
	restored := false
	if _, err := fixture.mgr.RebuildWorkerWorkspace(t.Context(), binding.Root, func(_ context.Context, _ SandboxLayout, root string) error {
		restored = true
		if root != binding.Root {
			t.Fatalf("rebuilt at %s, want %s", root, binding.Root)
		}
		return nil
	}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !restored {
		t.Fatal("restore did not run")
	}
	removed, err := ReconcileStaleSandboxes(t.Context(), fixture.mgr.branchRoot, project, func(context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{}, nil
	})
	testutil.FailErr(t, "reconcile", err)
	if removed == 0 {
		t.Fatal("reconciliation did not find the placed branch")
	}
	if _, err := os.Lstat(binding.Root); !os.IsNotExist(err) {
		t.Fatalf("reconciled branch remains: %v", err)
	}
	registry, err := workerplacement.Load(fixture.state)
	testutil.FailErr(t, "registry", err)
	if len(registry.Branches) != 0 {
		t.Fatalf("records outlived the branch: %+v", registry.Branches)
	}
}

func TestStateFolderTreeWinsOverAStaleRecordAfterRestore(t *testing.T) {
	volume := testvolume.New(t, testvolume.Movable)
	t.Cleanup(UseCopyAsCloneForTest())
	project := volume.Dir(t, "project")
	writeTree(t, project, projectFiles)
	fixture := newPlacementFixture(t)
	binding, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), project, "job")
	testutil.FailErr(t, "create", err)
	restored := fixture.mgr.jobRoot(project, "job")
	testutil.FailErr(t, "restore tree", os.MkdirAll(restored, 0o750))

	logical, ok, err := workerplacement.LogicalFromPhysical(fixture.state, binding.Root)
	testutil.FailErr(t, "logical", err)
	if !ok {
		t.Fatal("placed branch has no logical address")
	}
	physical, err := workerplacement.PhysicalFromLogical(fixture.state, logical)
	testutil.FailErr(t, "physical", err)
	if physical != restored {
		t.Fatalf("physical = %s, want the restored tree %s", physical, restored)
	}
	removed, err := ReconcileStaleSandboxes(t.Context(), fixture.mgr.branchRoot, project, func(context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{"job": {}}, nil
	})
	testutil.FailErr(t, "reconcile", err)
	if removed != 1 {
		t.Fatalf("removed = %d", removed)
	}
	if _, err := os.Lstat(binding.Root); !os.IsNotExist(err) {
		t.Fatalf("superseded placed tree remains: %v", err)
	}
	if _, err := os.Lstat(restored); err != nil {
		t.Fatalf("restored tree was removed: %v", err)
	}
}

func TestStateFolderOnSourceVolumeOutsideRootsKeepsTheBranch(t *testing.T) {
	project := t.TempDir()
	writeTree(t, project, projectFiles)
	fixture := newPlacementFixture(t)
	binding, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), project, "job")
	testutil.FailErr(t, "create", err)
	if binding.Preparation.PlacementFolder != "" {
		t.Fatalf("placed although the state folder qualifies: %+v", binding.Preparation)
	}
	if !strings.HasPrefix(binding.Root, enginepaths.WorkerBranchesRootUnder(fixture.state)) {
		t.Fatalf("branch left the state folder: %s", binding.Root)
	}
}

func TestUnreadableEntriesAreCountedAndAbsent(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	project := t.TempDir()
	writeTree(t, project, map[string]string{"kept.txt": "k", "closed/inner.txt": "i", "locked.txt": "l"})
	closed, locked := filepath.Join(project, "closed"), filepath.Join(project, "locked.txt")
	testutil.FailErr(t, "close directory", os.Chmod(closed, 0))
	testutil.FailErr(t, "lock file", os.Chmod(locked, 0))
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
	fixture := newPlacementFixture(t)

	binding, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), project, "job")
	testutil.FailErr(t, "create", err)
	if got := binding.Preparation.Roots[0].UnreadableEntries; got != 2 {
		t.Fatalf("unreadable entries = %d", got)
	}
	if _, err := os.Lstat(filepath.Join(binding.Root, "kept.txt")); err != nil {
		t.Fatalf("readable file missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(binding.Root, "locked.txt")); !os.IsNotExist(err) {
		t.Fatalf("unreadable file present: %v", err)
	}
}
