package session_test

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOverlayChangedPathsAndDivergenceFromPublishedCheckpoint(t *testing.T) {
	primary, overlay := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "write primary", os.WriteFile(filepath.Join(primary, "checkpointed.go"), []byte("package p\n// v1\n"), 0o644))
	testutil.FailErr(t, "write deleted", os.WriteFile(filepath.Join(primary, "deleted.go"), []byte("package p\n// del\n"), 0o644))
	store, bindCheckpoint := recoveryStoreFor(t, overlay, primary)
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-session-cp", BackingDir: t.TempDir(),
		Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "checkpointed.go"}, []byte("package p\n// v2\n"), 0o644)
	testutil.FailErr(t, "WriteFile checkpointed", err)
	testutil.FailErr(t, "Unlink deleted", core.Unlink(layeredfs.QualifiedPath{RootID: "primary", Rel: "deleted.go"}, false))
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		WorkerID: "job-cp-session", TaskID: "job-cp-session", ProjectID: testdbseed.DefaultProjectID,
		SourceGeneration: "gen-1", Core: core, Store: store,
	})
	testutil.FailErr(t, "NewPublisher", err)
	_, err = pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "chk-session-1", Trigger: checkpoint.TriggerWorkerCompletion})
	testutil.FailErr(t, "Publish", err)
	bindCheckpoint("chk-session-1")
	testutil.FailErr(t, "write uncheckpointed", os.WriteFile(filepath.Join(overlay, "uncheckpointed.go"), []byte("package p\n// dirty\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite}
	task := &api.WorkerTask{ID: "job-cp-session", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary, WorkspaceRoot: overlay, Scope: &scope}
	if !session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected OverlayAwaitingPromote true for published checkpoint")
	}
	changed := session.OverlayWorkspaceChangedPaths(t.Context(), task, nil)
	if len(changed) != 2 || changed[0] != "checkpointed.go" || changed[1] != "deleted.go" {
		t.Fatalf("OverlayWorkspaceChangedPaths = %v, want [checkpointed.go deleted.go]", changed)
	}
	div := session.OverlayWorkspaceDivergencePaths(task, nil)
	if len(div) != 2 || div[0] != "checkpointed.go" || div[1] != "deleted.go" {
		t.Fatalf("OverlayWorkspaceDivergencePaths = %v, want [checkpointed.go deleted.go]", div)
	}
}

func TestCheckpointRefImportValidatesReachableObjectsConflictsAndWorktreeGitdir(t *testing.T) {
	baseDir := t.TempDir()
	commonGit, worktreeGit, primary := filepath.Join(baseDir, "common-git"), filepath.Join(baseDir, "worktree-git"), filepath.Join(baseDir, "worktree")
	for _, d := range []string{filepath.Join(commonGit, "objects"), worktreeGit, primary} {
		testutil.FailErr(t, "MkdirAll", os.MkdirAll(d, 0o755))
	}
	testutil.FailErr(t, "write .git file", os.WriteFile(filepath.Join(primary, ".git"), []byte("gitdir: ../worktree-git\n"), 0o644))
	testutil.FailErr(t, "write commondir", os.WriteFile(filepath.Join(worktreeGit, "commondir"), []byte("../common-git\n"), 0o644))

	storeDir := filepath.Join(enginepaths.WorkerRecoveryRootUnder(baseDir), "checkpoints")
	_, err := checkpoint.NewStore(storeDir)
	testutil.FailErr(t, "NewStore", err)

	danglingOID := "1111111111111111111111111111111111111111"
	unauthImporter := func(ctx context.Context, _, worker string, git checkpoint.PrivateGitSnapshot) (map[string]string, error) {
		return checkpoint.ImportAndValidateCheckpointGitRefs(ctx, checkpoint.GitObjectSources{StoreDir: storeDir}, "chk-1", worker, git, map[string]string{"primary": primary})
	}
	if _, err := unauthImporter(t.Context(), "tx-1", "w-1", checkpoint.PrivateGitSnapshot{Refs: map[string]string{"refs/heads/feat": danglingOID}}); !errors.Is(err, checkpoint.ErrUnauthorizedGitDir) {
		t.Fatalf("expected ErrUnauthorizedGitDir for external worktree gitdir without grant, got %v", err)
	}
	importer := func(ctx context.Context, _, worker string, git checkpoint.PrivateGitSnapshot) (map[string]string, error) {
		return checkpoint.ImportAndValidateCheckpointGitRefs(ctx, checkpoint.GitObjectSources{StoreDir: storeDir}, "chk-1", worker, git, map[string]string{"primary": primary}, worktreeGit, commonGit)
	}
	if _, err := importer(t.Context(), "tx-1", "w-1", checkpoint.PrivateGitSnapshot{Refs: map[string]string{"refs/heads/feat": danglingOID}}); err == nil {
		t.Fatal("expected error for dangling object OID, got nil")
	}
	if _, err := os.Stat(filepath.Join(commonGit, "refs", "lycaon", "workers", "w-1", "heads", "feat")); !os.IsNotExist(err) {
		t.Fatalf("dangling ref must not be written, stat err = %v", err)
	}

	objsDir := checkpoint.CheckpointGitObjectsDir(storeDir, "chk-1", "")
	blobOID, err := checkpoint.WriteLooseGitObject(objsDir, "blob", []byte("hello world\n"))
	testutil.FailErr(t, "write blob", err)
	blobRaw, _ := hex.DecodeString(blobOID)
	treeOID, err := checkpoint.WriteLooseGitObject(objsDir, "tree", append([]byte("100644 file.txt\x00"), blobRaw...))
	testutil.FailErr(t, "write tree", err)
	commitOID, err := checkpoint.WriteLooseGitObject(objsDir, "commit", []byte(fmt.Sprintf("tree %s\nauthor A <a@b.c> 1 +0000\ncommitter A <a@b.c> 1 +0000\n\nmsg\n", treeOID)))
	testutil.FailErr(t, "write commit", err)

	imported, err := importer(t.Context(), "tx-1", "w-1", checkpoint.PrivateGitSnapshot{Refs: map[string]string{"refs/heads/feat": commitOID}})
	testutil.FailErr(t, "valid ref import", err)
	if imported["refs/lycaon/workers/w-1/heads/feat"] != commitOID || imported["refs/lycaon/checkpoints/chk-1/heads/feat"] != commitOID {
		t.Fatalf("unexpected imported refs: %v", imported)
	}
	if _, err := importer(t.Context(), "tx-1", "w-1", checkpoint.PrivateGitSnapshot{Refs: map[string]string{"refs/heads/feat": commitOID}}); err != nil {
		t.Fatalf("same-OID retry failed: %v", err)
	}
	otherOID, err := checkpoint.WriteLooseGitObject(objsDir, "blob", []byte("other\n"))
	testutil.FailErr(t, "write other blob", err)
	if _, err := importer(t.Context(), "tx-1", "w-1", checkpoint.PrivateGitSnapshot{Refs: map[string]string{"refs/heads/feat": otherOID}}); !errors.Is(err, checkpoint.ErrCheckpointRefConflict) {
		t.Fatalf("conflicting OID err = %v, want ErrCheckpointRefConflict", err)
	}
}

func TestCheckpointPromotionRetryUnresolvedRootAndErrorPropagation(t *testing.T) {
	primary := t.TempDir()
	removedPath := filepath.Join(primary, "removed.txt")
	testutil.FailErr(t, "seed removed.txt", os.WriteFile(removedPath, []byte("old\n"), 0o644))

	overlay := t.TempDir()
	store, bindCheckpoint := recoveryStoreFor(t, overlay, primary)

	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-retry", BackingDir: t.TempDir(),
		Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	testutil.FailErr(t, "Unlink removed.txt", core.Unlink(layeredfs.QualifiedPath{RootID: "primary", Rel: "removed.txt"}, false))
	_, err = core.Mkdir(layeredfs.QualifiedPath{RootID: "primary", Rel: "sub"}, 0o755)
	testutil.FailErr(t, "Mkdir sub", err)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "sub/added.sh"}, []byte("#!/bin/sh\n"), 0o755)
	testutil.FailErr(t, "WriteFile sub/added.sh", err)

	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		WorkerID: "job-retry", TaskID: "job-retry", ProjectID: testdbseed.DefaultProjectID,
		SourceGeneration: "gen-1", Core: core, Store: store,
	})
	testutil.FailErr(t, "NewPublisher", err)
	pubRes, err := pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "chk-retry-1", Trigger: checkpoint.TriggerWorkerCompletion})
	testutil.FailErr(t, "Publish", err)
	bindCheckpoint("chk-retry-1")

	task := &api.WorkerTask{ID: "job-retry", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary, WorkspaceRoot: overlay}
	badManifest := pubRes.Manifest
	badManifest.Entries = append([]checkpoint.ManifestEntry(nil), badManifest.Entries...)
	badManifest.Entries[0].Path.RootID = "unmapped-secondary"
	if _, err := session.TargetRootsForTaskCheckpoint(task, nil, badManifest); !errors.Is(err, layeredfs.ErrInvalidRoot) {
		t.Fatalf("TargetRootsForTaskCheckpoint unmapped root err = %v, want ErrInvalidRoot", err)
	}

	testutil.FailErr(t, "commit task checkpoint", commitTaskCheckpoint(t, task, nil))
	if _, err := os.Stat(removedPath); !os.IsNotExist(err) {
		t.Fatalf("expected removed.txt to be deleted, stat err = %v", err)
	}

	// Recreate file at old deletion path after promotion completes; retry must NOT re-delete it.
	testutil.FailErr(t, "recreate removed.txt", os.WriteFile(removedPath, []byte("recreated post-promote\n"), 0o644))
	testutil.FailErr(t, "retry task checkpoint", commitTaskCheckpoint(t, task, nil))
	if gotBytes, err := os.ReadFile(removedPath); err != nil || !strings.Contains(string(gotBytes), "recreated post-promote") {
		t.Fatalf("post-promotion recreated file was clobbered or unreadable: %q (%v)", string(gotBytes), err)
	}
}

func TestMultiRootCheckpointQualificationAndWorkspaceSync(t *testing.T) {
	primary, secondary, overlay := t.TempDir(), t.TempDir(), t.TempDir()
	testutil.FailErr(t, "seed secondary", os.WriteFile(filepath.Join(secondary, "mod.txt"), []byte("v1\n"), 0o644))
	store, bindCheckpoint := recoveryStoreFor(t, overlay, primary)

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:     "view-multiroot-cp",
		BackingDir: t.TempDir(),
		Roots: []layeredfs.RootSpec{
			{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true},
			{ID: "root-sec", Label: "docs", TrunkPath: secondary},
		},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "root-sec", Rel: "mod.txt"}, []byte("v2\n"), 0o644)
	testutil.FailErr(t, "WriteFile secondary", err)

	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		WorkerID: "job-multi-cp", TaskID: "job-multi-cp", ProjectID: testdbseed.DefaultProjectID,
		SourceGeneration: "gen-1", Core: core, Store: store,
	})
	testutil.FailErr(t, "NewPublisher", err)
	_, err = pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "chk-multi-1", Trigger: checkpoint.TriggerWorkerCompletion})
	testutil.FailErr(t, "Publish", err)
	bindCheckpoint("chk-multi-1")

	roots := []projectroot.RootRef{
		{ID: "primary", Label: "primary", Path: primary, IsPrimary: true},
		{ID: "root-sec", Label: "docs", Path: secondary},
	}
	if got := session.QualifyCheckpointPath(layeredfs.QualifiedPath{RootID: "root-sec", Rel: "mod.txt"}, roots); got != "@docs/mod.txt" {
		t.Fatalf("QualifyCheckpointPath = %q, want @docs/mod.txt", got)
	}

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite}
	task := &api.WorkerTask{ID: "job-multi-cp", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary, WorkspaceRoot: overlay, Scope: &scope}
	changed := session.OverlayWorkspaceChangedPaths(t.Context(), task, roots)
	if len(changed) != 1 || changed[0] != "@docs/mod.txt" {
		t.Fatalf("OverlayWorkspaceChangedPaths = %v, want [@docs/mod.txt]", changed)
	}
	testutil.FailErr(t, "SyncTaskCheckpointToWorkspace", session.SyncTaskCheckpointToWorkspace(task, roots))
	secBranchDir, err := projectroot.BranchDirForID("root-sec")
	testutil.FailErr(t, "BranchDirForID", err)
	syncedPath := filepath.Join(overlay, secBranchDir, "mod.txt")
	if got, err := os.ReadFile(syncedPath); err != nil || string(got) != "v2\n" {
		t.Fatalf("synced secondary branch file = %q, err = %v", string(got), err)
	}
}

func publishCheckpointForSync(t *testing.T, overlay string, roots []layeredfs.RootSpec, mutate func(*layeredfs.Core)) {
	t.Helper()
	store, bindCheckpoint := recoveryStoreFor(t, overlay, roots[0].TrunkPath)
	core, err := layeredfs.New(layeredfs.Config{ViewID: "view-sync", BackingDir: t.TempDir(), Roots: roots})
	testutil.FailErr(t, "New Core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })
	mutate(core)
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{WorkerID: "job-sync", TaskID: "job-sync", ProjectID: testdbseed.DefaultProjectID, SourceGeneration: "gen-1", Core: core, Store: store})
	testutil.FailErr(t, "NewPublisher", err)
	_, err = pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "chk-sync", Trigger: checkpoint.TriggerWorkerCompletion})
	testutil.FailErr(t, "Publish", err)
	bindCheckpoint("chk-sync")
}

func TestSyncTaskCheckpointBindsEntriesToTheBranchLayout(t *testing.T) {
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite}
	write := func(t *testing.T, core *layeredfs.Core, rootID string) {
		t.Helper()
		_, err := core.WriteFile(layeredfs.QualifiedPath{RootID: rootID, Rel: "a.txt"}, []byte("worker\n"), 0o644)
		testutil.FailErr(t, "worker write", err)
	}
	t.Run("single root is flat under its real id", func(t *testing.T) {
		primary, overlay := t.TempDir(), t.TempDir()
		publishCheckpointForSync(t, overlay, []layeredfs.RootSpec{{ID: "root-main", TrunkPath: primary, IsPrimary: true}}, func(core *layeredfs.Core) { write(t, core, "root-main") })
		task := &api.WorkerTask{ID: "job-sync", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary, WorkspaceRoot: overlay, Scope: &scope}
		roots := []projectroot.RootRef{{ID: "root-main", Path: primary, IsPrimary: true}}
		testutil.FailErr(t, "sync", session.SyncTaskCheckpointToWorkspace(task, roots))
		if got, err := os.ReadFile(filepath.Join(overlay, "a.txt")); err != nil || string(got) != "worker\n" {
			t.Fatalf("flat branch file = %q, %v", got, err)
		}
	})
	t.Run("primary alias lands in the primary branch directory", func(t *testing.T) {
		primary, secondary, overlay := t.TempDir(), t.TempDir(), t.TempDir()
		publishCheckpointForSync(t, overlay, []layeredfs.RootSpec{
			{ID: "primary", TrunkPath: primary, IsPrimary: true},
			{ID: "root-sec", TrunkPath: secondary},
		}, func(core *layeredfs.Core) { write(t, core, "primary") })
		task := &api.WorkerTask{ID: "job-sync", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary, WorkspaceRoot: overlay, WorkspaceRootID: "root-main", Scope: &scope}
		roots := []projectroot.RootRef{{ID: "root-main", Path: primary, IsPrimary: true}, {ID: "root-sec", Path: secondary}}
		testutil.FailErr(t, "sync", session.SyncTaskCheckpointToWorkspace(task, roots))
		dir, err := projectroot.BranchDirForID("root-main")
		testutil.FailErr(t, "branch dir", err)
		if got, err := os.ReadFile(filepath.Join(overlay, dir, "a.txt")); err != nil || string(got) != "worker\n" {
			t.Fatalf("primary branch file = %q, %v", got, err)
		}
	})
	t.Run("deleted child beneath a replaced symlink stays confined", func(t *testing.T) {
		primary, overlay, outside := t.TempDir(), t.TempDir(), t.TempDir()
		testutil.FailErr(t, "seed lower dir", os.MkdirAll(filepath.Join(primary, "d"), 0o755))
		testutil.FailErr(t, "seed lower child", os.WriteFile(filepath.Join(primary, "d", "child.txt"), []byte("lower\n"), 0o644))
		victim := filepath.Join(outside, "child.txt")
		testutil.FailErr(t, "seed outside file", os.WriteFile(victim, []byte("outside\n"), 0o644))
		publishCheckpointForSync(t, overlay, []layeredfs.RootSpec{{ID: "primary", TrunkPath: primary, IsPrimary: true}}, func(core *layeredfs.Core) {
			qp := func(rel string) layeredfs.QualifiedPath { return layeredfs.QualifiedPath{RootID: "primary", Rel: rel} }
			testutil.FailErr(t, "remove lower dir", core.RemoveAll(qp("d")))
			_, err := core.Symlink(outside, qp("d"))
			testutil.FailErr(t, "replace with symlink", err)
		})
		task := &api.WorkerTask{ID: "job-sync", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary, WorkspaceRoot: overlay, Scope: &scope}
		for range 2 {
			testutil.FailErr(t, "sync", session.SyncTaskCheckpointToWorkspace(task, nil))
			if got, err := os.ReadFile(victim); err != nil || string(got) != "outside\n" {
				t.Fatalf("sync escaped the branch: %q, %v", got, err)
			}
		}
		if got, err := os.Readlink(filepath.Join(overlay, "d")); err != nil || got != outside {
			t.Fatalf("rehydrated symlink = %q, %v", got, err)
		}
	})
	t.Run("unknown root in a multi-root project refuses", func(t *testing.T) {
		primary, secondary, overlay := t.TempDir(), t.TempDir(), t.TempDir()
		publishCheckpointForSync(t, overlay, []layeredfs.RootSpec{{ID: "root-gone", TrunkPath: secondary}}, func(core *layeredfs.Core) { write(t, core, "root-gone") })
		task := &api.WorkerTask{ID: "job-sync", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary, WorkspaceRoot: overlay, Scope: &scope}
		roots := []projectroot.RootRef{{ID: "root-main", Path: primary, IsPrimary: true}, {ID: "root-sec", Path: t.TempDir()}}
		if err := session.SyncTaskCheckpointToWorkspace(task, roots); !errors.Is(err, layeredfs.ErrInvalidRoot) {
			t.Fatalf("sync = %v, want ErrInvalidRoot", err)
		}
	})
}

// recoveryStoreFor opens a store at the recovery location and returns a function naming a published
// checkpoint as a branch's result, as completion does.
func recoveryStoreFor(t *testing.T, branch, source string) (*checkpoint.Store, func(checkpointID string)) {
	t.Helper()
	store, err := checkpoint.NewStore(filepath.Join(enginepaths.WorkerRecoveryRootUnder(t.TempDir()), "checkpoints"))
	testutil.FailErr(t, "open recovery checkpoint store", err)
	return store, func(checkpointID string) {
		t.Helper()
		meta := workspace.JobMeta{Roots: []workspace.JobMetaRoot{{ID: "primary", Path: source, IsPrimary: true}}, SnapshotComplete: true}
		meta.LastPublishedCheckpointID, meta.CheckpointStoreDir = checkpointID, store.RootDir()
		testutil.FailErr(t, "record branch checkpoint", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(filepath.Clean(branch)), meta))
	}
}

// commitTaskCheckpoint runs a task's checkpoint promotion through a complete transaction.
func commitTaskCheckpoint(t *testing.T, task *api.WorkerTask, roots []projectroot.RootRef) error {
	t.Helper()
	txn, err := checkpoint.BeginTransaction(checkpoint.TransactionConfig{Dir: filepath.Join(t.TempDir(), "tx"), ID: "promotion"})
	testutil.FailErr(t, "begin transaction", err)
	if err := session.ApplyTaskCheckpoint(t.Context(), txn, task, roots); err != nil {
		return err
	}
	return txn.Commit()
}

func TestCheckpointPromotionUsesBoundPreviewAfterNewPublication(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "newer checkpoint"
		if missing {
			name = "missing reviewed checkpoint"
		}
		t.Run(name, func(t *testing.T) {

			primary, overlay := t.TempDir(), t.TempDir()
			store, bindCheckpoint := recoveryStoreFor(t, overlay, primary)
			core, err := layeredfs.New(layeredfs.Config{
				ViewID: "view-bound-preview", BackingDir: t.TempDir(),
				Roots: []layeredfs.RootSpec{{ID: "primary", TrunkPath: primary, IsPrimary: true}},
			})
			testutil.FailErr(t, "create checkpoint core", err)
			t.Cleanup(func() { _ = core.ReleaseView() })
			pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
				WorkerID: "job-bound-preview", TaskID: "job-bound-preview", ProjectID: testdbseed.DefaultProjectID,
				SourceGeneration: "gen-bound-preview", Core: core, Store: store,
			})
			testutil.FailErr(t, "create checkpoint publisher", err)
			path := layeredfs.QualifiedPath{RootID: "primary", Rel: "result.txt"}
			_, err = core.WriteFile(path, []byte("reviewed\n"), 0o644)
			testutil.FailErr(t, "write reviewed result", err)
			_, err = pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "cp-reviewed", Trigger: checkpoint.TriggerWorkerCompletion})
			testutil.FailErr(t, "publish reviewed checkpoint", err)
			bindCheckpoint("cp-reviewed")
			task := &api.WorkerTask{ID: "job-bound-preview", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary, WorkspaceRoot: overlay}
			_, manifest, promoter, ok, err := session.NewTaskCheckpointPromoter(task, nil)
			testutil.FailErr(t, "prepare reviewed checkpoint", err)
			if !ok {
				t.Fatal("published checkpoint was not selected")
			}
			preview, err := promoter.Preview(t.Context(), checkpoint.PreviewRequest{CheckpointID: manifest.CheckpointID})
			testutil.FailErr(t, "preview reviewed checkpoint", err)
			session.MarkTaskCheckpointPreviewBindIntent(task.ID)
			session.MaybeBindTaskCheckpointPreview(task.ID, preview)
			t.Cleanup(func() { session.ClearTaskCheckpointPreview(task.ID) })
			_, err = core.WriteFile(path, []byte("later\n"), 0o644)
			testutil.FailErr(t, "write later result", err)
			_, err = pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "cp-later", Trigger: checkpoint.TriggerWorkerCompletion})
			testutil.FailErr(t, "publish later checkpoint", err)
			if missing {
				testutil.FailErr(t, "remove reviewed manifest", os.Remove(filepath.Join(store.RootDir(), "checkpoints", "cp-reviewed", "manifest.json")))
			}
			_, selected, _, ok, err := session.NewTaskCheckpointPromoter(task, nil)
			if missing {
				if !ok || !errors.Is(err, checkpoint.ErrCheckpointNotFound) {
					t.Fatalf("missing reviewed checkpoint fell back: selected=%q ok=%t err=%v", selected.CheckpointID, ok, err)
				}
				return
			}
			testutil.FailErr(t, "load bound checkpoint", err)
			if !ok || selected.CheckpointID != preview.CheckpointID {
				t.Fatalf("checkpoint changed after preview: selected=%q reviewed=%q", selected.CheckpointID, preview.CheckpointID)
			}
			testutil.FailErr(t, "commit reviewed checkpoint", commitTaskCheckpoint(t, task, nil))
			body, err := os.ReadFile(filepath.Join(primary, "result.txt"))
			testutil.FailErr(t, "read promoted result", err)
			if string(body) != "reviewed\n" {
				t.Fatalf("promoted unreviewed checkpoint bytes %q", body)
			}

		})
	}
}

func TestOverlayPromoteConsultsLayeredConsumerDivergenceWithoutFullBaseline(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "source")
	mountDir := filepath.Join(tmp, "mount")
	backingDir := filepath.Join(tmp, "backing")
	for _, d := range []string{srcDir, mountDir, backingDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	roots := []layeredfs.RootSpec{{ID: "code", Label: "code", TrunkPath: srcDir, IsPrimary: true}}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-session-promote", BackingDir: backingDir, Roots: roots,
	})
	if err != nil {
		t.Fatalf("layeredfs.New: %v", err)
	}
	t.Cleanup(func() { _ = core.ReleaseView() })

	if _, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "code", Rel: "main.go"}, []byte("package main\n// edited\n"), 0o644); err != nil {
		t.Fatalf("WriteFile main.go: %v", err)
	}
	if _, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "code", Rel: "feature.go"}, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile feature.go: %v", err)
	}

	eng, err := consumer.NewEngine(consumer.EngineConfig{
		ViewID:           "view-session-promote",
		Roots:            roots,
		MountedRootPaths: map[string]string{"code": mountDir},
		BackingDir:       backingDir,
		Core:             core,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	consumer.RegisterBranchEngine(mountDir, eng)
	t.Cleanup(func() { consumer.UnregisterBranchEngine(mountDir) })

	task := &api.WorkerTask{
		ID:            "job-layered-promote",
		Scope:         &api.TaskScope{Mode: api.TaskScopeModeWrite},
		WorkspaceRoot: mountDir,
		WorkspacePath: srcDir,
		// WorkspaceBaselinePath intentionally empty to prove layered O(changed) divergence is used.
	}
	rootRefs := []projectroot.RootRef{{ID: "code", Label: "code", Path: srcDir, IsPrimary: true}}

	changed, err := session.InspectOverlayChanges(ctx, task, rootRefs)
	if err != nil {
		t.Fatalf("InspectOverlayChanges: %v", err)
	}
	want := []string{"feature.go", "main.go"}
	if !reflect.DeepEqual(changed, want) {
		t.Fatalf("InspectOverlayChanges = %v, want %v", changed, want)
	}

	div := session.OverlayWorkspaceDivergencePaths(task, rootRefs)
	if !reflect.DeepEqual(div, want) {
		t.Fatalf("OverlayWorkspaceDivergencePaths = %v, want %v", div, want)
	}
	if !session.OverlayAwaitingPromote(ctx, task) {
		t.Fatalf("expected OverlayAwaitingPromote=true when layered view has private changes")
	}
}
