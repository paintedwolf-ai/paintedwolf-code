package session_test

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOverlayChangedPathsHonorsCallerCancellation(t *testing.T) {
	root := t.TempDir()
	baseline := testbaseline.Capture(t, root)
	testutil.FailErr(t, "write change", os.WriteFile(filepath.Join(root, "changed.go"), []byte("changed\n"), 0o600))
	task := &api.WorkerTask{
		WorkspaceRoot: root, WorkspaceBaselinePath: baseline,
		Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite},
	}
	if paths := session.OverlayWorkspaceChangedPaths(t.Context(), task, nil); len(paths) != 1 {
		t.Fatalf("live inspection paths=%v", paths)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if paths := session.OverlayWorkspaceChangedPaths(ctx, task, nil); len(paths) != 0 {
		t.Fatalf("canceled inspection continued: %v", paths)
	}
	if !session.OverlayAwaitingPromote(ctx, task) {
		t.Fatal("canceled inspection treated unresolved work as clean")
	}
}

func TestOverlayChangedPathsIncludesChangesOutsideSuggestedScope(t *testing.T) {
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-a")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "a.go"), []byte("branch\n"), 0o644))
	testutil.FailErr(t, "WriteFile extra", os.WriteFile(filepath.Join(overlay, "b.go"), []byte("extra\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	baseline := testbaseline.Capture(t, primary)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}

	paths := session.OverlayWorkspaceChangedPaths(t.Context(), task, nil)
	if len(paths) != 2 || paths[0] != "a.go" || paths[1] != "b.go" {
		t.Fatalf("OverlayWorkspaceChangedPaths = %v want [a.go b.go]", paths)
	}
	if !session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected overlay awaiting promote")
	}
}

func TestOverlayAwaitingPromoteWithAbsolutePathSuggestion(t *testing.T) {
	primary := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "index.html"), []byte("v1"), 0o644))

	overlay := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "index.html"), []byte("v2-rewritten"), 0o644))
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Join(overlay, "src"), 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "src", "game.js"), []byte("game"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{primary}}
	baseline := testbaseline.Capture(t, primary)
	if len(baseline) == 0 {
		t.Fatalf("baseline = %+v", baseline)
	}

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	if !session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("whole-repo absolute scope overlay must await promote, not abort")
	}
	paths := session.OverlayWorkspaceChangedPaths(t.Context(), task, nil)
	if len(paths) != 2 {
		t.Fatalf("changed = %v want index.html + src/game.js", paths)
	}
}

func TestOverlayDivergencePathsSafetyNet(t *testing.T) {
	primary := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "index.html"), []byte("v1"), 0o644))

	overlay := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "index.html"), []byte("v2-rewritten"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "extra.js"), []byte("new"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"untouched.txt"}}
	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot: overlay,
		Scope:         &scope,
	}
	div := session.OverlayWorkspaceDivergencePaths(task, nil)
	if len(div) != 2 {
		t.Fatalf("divergence = %v want index.html + extra.js", div)
	}
}

func TestOverlayAwaitingPromoteFalseWithoutDiff(t *testing.T) {
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-b")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "a.go"), []byte("same\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	baseline := testbaseline.Capture(t, overlay)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	if session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected no promote when overlay matches baseline")
	}
}

func TestOverlayWorkspaceAvailableRejectsSymlinkRoot(t *testing.T) {
	branch := filepath.Join(t.TempDir(), "branch")
	if err := os.Symlink(t.TempDir(), branch); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if session.OverlayWorkspaceAvailable(&api.WorkerTask{WorkspaceRoot: branch}) {
		t.Fatal("OverlayWorkspaceAvailable accepted a symlink root")
	}
}

func TestOverlayAwaitingPromoteFalseWithoutChanges(t *testing.T) {
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-absent")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"test-chess.js"}}
	baseline := testbaseline.Capture(t, primary)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot: overlay, Scope: &scope, WorkspaceBaselinePath: baseline,
	}
	if session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected no promote without changes")
	}
}

func TestOverlayAwaitingPromoteFalseWhenWorkspaceGone(t *testing.T) {
	primary := t.TempDir()
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	baseline := testbaseline.Capture(t, primary)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot: filepath.Join(primary, settingsoverlay.DirName(), "overlays", "gone"),
		Scope: &scope, WorkspaceBaselinePath: baseline,
	}
	if session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected false when branch directory is missing")
	}
}

func TestResolveWorkerSummaryStatus(t *testing.T) {
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-c")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "a.go"), []byte("new\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	baseline := testbaseline.Capture(t, primary)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot: overlay, Scope: &scope, WorkspaceBaselinePath: baseline,
	}
	if got := session.ResolveWorkerSummaryStatus(t.Context(), api.WorkerSummaryStatusComplete, task); got != api.WorkerSummaryStatusOpen {
		t.Fatalf("eligible status = %q want open", got)
	}
}

func TestOverlayChangedPathsAndDivergenceFromPublishedCheckpoint(t *testing.T) {
	primary := t.TempDir()
	testutil.FailErr(t, "write primary", os.WriteFile(filepath.Join(primary, "checkpointed.go"), []byte("package p\n// v1\n"), 0o644))
	testutil.FailErr(t, "write deleted", os.WriteFile(filepath.Join(primary, "deleted.go"), []byte("package p\n// del\n"), 0o644))

	overlay := t.TempDir()
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

	_, err = pub.Publish(t.Context(), checkpoint.PublishRequest{
		CheckpointID: "chk-session-1", Trigger: checkpoint.TriggerWorkerCompletion,
	})
	testutil.FailErr(t, "Publish", err)

	testutil.FailErr(t, "write uncheckpointed", os.WriteFile(filepath.Join(overlay, "uncheckpointed.go"), []byte("package p\n// dirty\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite}
	task := &api.WorkerTask{
		ID: "job-cp-session", ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: primary, WorkspaceRoot: overlay, Scope: &scope,
	}

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
	commonGit := filepath.Join(baseDir, "common-git")
	worktreeGit := filepath.Join(baseDir, "worktree-git")
	primary := filepath.Join(baseDir, "worktree")
	for _, d := range []string{filepath.Join(commonGit, "objects"), worktreeGit, primary} {
		testutil.FailErr(t, "MkdirAll", os.MkdirAll(d, 0o755))
	}
	testutil.FailErr(t, "write .git file", os.WriteFile(filepath.Join(primary, ".git"), []byte("gitdir: ../worktree-git\n"), 0o644))
	testutil.FailErr(t, "write commondir", os.WriteFile(filepath.Join(worktreeGit, "commondir"), []byte("../common-git\n"), 0o644))

	storeDir := filepath.Join(baseDir, "overlay", session.TaskCheckpointStoreSubdir)
	store, err := checkpoint.NewStore(storeDir)
	testutil.FailErr(t, "NewStore", err)

	danglingOID := "1111111111111111111111111111111111111111"
	importer := session.DefaultCheckpointRefImporter(store, checkpoint.Manifest{CheckpointID: "chk-1", WorkerID: "w-1"}, map[string]string{"primary": primary})
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
	treePayload := append([]byte("100644 file.txt\x00"), blobRaw...)
	treeOID, err := checkpoint.WriteLooseGitObject(objsDir, "tree", treePayload)
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

	testutil.FailErr(t, "CommitTaskCheckpointJournal", session.CommitTaskCheckpointJournal(t.Context(), task, nil, nil))
	if _, err := os.Stat(removedPath); !os.IsNotExist(err) {
		t.Fatalf("expected removed.txt to be deleted, stat err = %v", err)
	}

	// Recreate file at old deletion path after promotion completes; retry must NOT re-delete it.
	testutil.FailErr(t, "recreate removed.txt", os.WriteFile(removedPath, []byte("recreated post-promote\n"), 0o644))
	testutil.FailErr(t, "retry CommitTaskCheckpointJournal", session.CommitTaskCheckpointJournal(t.Context(), task, nil, nil))
	testutil.FailErr(t, "retry ApplyTaskCheckpointAttributes", session.ApplyTaskCheckpointAttributes(task, nil))
	gotBytes, err := os.ReadFile(removedPath)
	testutil.FailErr(t, "ReadFile recreated removed.txt", err)
	if !strings.Contains(string(gotBytes), "recreated post-promote") {
		t.Fatalf("post-promotion recreated file was clobbered: %q", string(gotBytes))
	}
}
