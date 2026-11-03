package session_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverlayChangedPathsAndDivergenceFromPublishedCheckpoint(t *testing.T) {
	primary, overlay := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "write primary", os.WriteFile(filepath.Join(primary, "checkpointed.go"), []byte("package p\n// v1\n"), 0o644))
	testutil.FailErr(t, "write deleted", os.WriteFile(filepath.Join(primary, "deleted.go"), []byte("package p\n// del\n"), 0o644))
	store, err := checkpoint.NewStore(filepath.Join(overlay, session.TaskCheckpointStoreSubdir))
	testutil.FailErr(t, "NewStore", err)
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

	storeDir := filepath.Join(baseDir, "overlay", session.TaskCheckpointStoreSubdir)
	store, err := checkpoint.NewStore(storeDir)
	testutil.FailErr(t, "NewStore", err)

	danglingOID := "1111111111111111111111111111111111111111"
	unauthImporter := session.DefaultCheckpointRefImporter(store, checkpoint.Manifest{CheckpointID: "chk-1", WorkerID: "w-1"}, map[string]string{"primary": primary})
	if _, err := unauthImporter(t.Context(), "tx-1", "w-1", checkpoint.PrivateGitSnapshot{Refs: map[string]string{"refs/heads/feat": danglingOID}}); !errors.Is(err, checkpoint.ErrUnauthorizedGitDir) {
		t.Fatalf("expected ErrUnauthorizedGitDir for external worktree gitdir without grant, got %v", err)
	}
	importer := session.DefaultCheckpointRefImporter(store, checkpoint.Manifest{CheckpointID: "chk-1", WorkerID: "w-1"}, map[string]string{"primary": primary}, worktreeGit, commonGit)
	if _, err := importer(t.Context(), "tx-1", "w-1", checkpoint.PrivateGitSnapshot{Refs: map[string]string{"refs/heads/feat": danglingOID}}); err == nil {
		t.Fatal("expected error for dangling object OID, got nil")
	}
	if _, err := os.Stat(filepath.Join(commonGit, "refs", "lycaon", "workers", "w-1", "heads", "feat")); !os.IsNotExist(err) {
		t.Fatalf("dangling ref must not be written, stat err = %v", err)
	}

	objsDir := filepath.Join(storeDir, "objects")
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
	store, err := checkpoint.NewStore(filepath.Join(overlay, session.TaskCheckpointStoreSubdir))
	testutil.FailErr(t, "NewStore", err)

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

	task := &api.WorkerTask{ID: "job-retry", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary, WorkspaceRoot: overlay}
	badManifest := pubRes.Manifest
	badManifest.Entries = append([]checkpoint.ManifestEntry(nil), badManifest.Entries...)
	badManifest.Entries[0].Path.RootID = "unmapped-secondary"
	if _, err := session.TargetRootsForTaskCheckpoint(task, nil, badManifest); !errors.Is(err, layeredfs.ErrInvalidRoot) {
		t.Fatalf("TargetRootsForTaskCheckpoint unmapped root err = %v, want ErrInvalidRoot", err)
	}

	// Verify filesystem error propagation when target parent directory cannot be created.
	blockedParent := filepath.Join(primary, "sub")
	testutil.FailErr(t, "write file blocking dir", os.WriteFile(blockedParent, []byte("blocker"), 0o644))
	if err := session.ApplyTaskCheckpointAttributes(task, nil); err == nil {
		t.Fatal("expected ApplyTaskCheckpointAttributes to propagate filesystem error when parent is a regular file")
	}
	testutil.FailErr(t, "remove blocker", os.Remove(blockedParent))
	testutil.FailErr(t, "re-seed removed.txt", os.WriteFile(removedPath, []byte("old\n"), 0o644))

	testutil.FailErr(t, "CommitTaskCheckpointJournal", session.CommitTaskCheckpointJournal(t.Context(), task, nil))
	if _, err := os.Stat(removedPath); !os.IsNotExist(err) {
		t.Fatalf("expected removed.txt to be deleted, stat err = %v", err)
	}

	// Recreate file at old deletion path after promotion completes; retry must NOT re-delete it.
	testutil.FailErr(t, "recreate removed.txt", os.WriteFile(removedPath, []byte("recreated post-promote\n"), 0o644))
	testutil.FailErr(t, "retry CommitTaskCheckpointJournal", session.CommitTaskCheckpointJournal(t.Context(), task, nil))
	testutil.FailErr(t, "retry ApplyTaskCheckpointAttributes", session.ApplyTaskCheckpointAttributes(task, nil))
	if gotBytes, err := os.ReadFile(removedPath); err != nil || !strings.Contains(string(gotBytes), "recreated post-promote") {
		t.Fatalf("post-promotion recreated file was clobbered or unreadable: %q (%v)", string(gotBytes), err)
	}
}

func TestMultiRootCheckpointQualificationAndWorkspaceSync(t *testing.T) {
	primary, secondary, overlay := t.TempDir(), t.TempDir(), t.TempDir()
	testutil.FailErr(t, "seed secondary", os.WriteFile(filepath.Join(secondary, "mod.txt"), []byte("v1\n"), 0o644))
	store, err := checkpoint.NewStore(filepath.Join(overlay, session.TaskCheckpointStoreSubdir))
	testutil.FailErr(t, "NewStore", err)

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
	testutil.FailErr(t, "SyncTaskCheckpointToWorkspace", session.SyncTaskCheckpointToWorkspace(task))
	secBranchDir, err := projectroot.BranchDirForID("root-sec")
	testutil.FailErr(t, "BranchDirForID", err)
	syncedPath := filepath.Join(overlay, secBranchDir, "mod.txt")
	if got, err := os.ReadFile(syncedPath); err != nil || string(got) != "v2\n" {
		t.Fatalf("synced secondary branch file = %q, err = %v", string(got), err)
	}
}
