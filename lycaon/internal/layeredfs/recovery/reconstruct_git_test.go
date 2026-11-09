package recovery_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestApplyCheckpointToViewRestoresPrimaryAndSecondaryRootGitRefs(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := t.Context()

	trunkSecond := filepath.Join(fx.baseDir, "trunk-second")
	if err := os.MkdirAll(trunkSecond, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Init(t, trunkSecond)
	writeFile(t, filepath.Join(trunkSecond, "sec.txt"), "sec-v0\n")
	gittest.CommitAll(t, trunkSecond, "initial second")

	multiPrepareReq := func(viewID string) adapter.PrepareRequest {
		return adapter.PrepareRequest{
			ViewID:     viewID,
			BackingDir: filepath.Join(fx.baseDir, "backing-"+viewID),
			RuntimeDir: filepath.Join(fx.baseDir, "runtime"),
			Grants: []adapter.RootGrant{
				{RootID: "primary", Label: "primary", SourcePath: fx.trunkDir, IsPrimary: true},
				{RootID: "second", Label: "second", SourcePath: trunkSecond, IsPrimary: false},
			},
		}
	}

	const workerID = "worker-multi-git-rec"
	viewA, err := fx.mountedAdp.Prepare(ctx, multiPrepareReq("view-multi-a"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(context.Background(), viewA.ViewID) }()
	if err := fx.mountedAdp.BindPrivateGit(viewA.ViewID, workerID, fx.gitMgr); err != nil {
		t.Fatalf("BindPrivateGit viewA: %v", err)
	}

	// Commit on primary root and secondary root, then capture and publish checkpoint.
	_, _ = viewA.Core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "app.go"}, []byte("package main // primary-v1\n"), 0o644)
	primaryExec := privategit.NewGitExecutor(fx.gitMgr, viewA.ViewID, privategit.RemotePolicy{})
	if _, err := primaryExec.Run(ctx, ".", "checkout", "-b", "feat-primary"); err != nil {
		t.Fatalf("primary checkout: %v", err)
	}
	if _, err := primaryExec.Run(ctx, ".", "add", "app.go"); err != nil {
		t.Fatalf("primary add: %v", err)
	}
	if _, err := primaryExec.Commit(ctx, "primary commit"); err != nil {
		t.Fatalf("primary commit: %v", err)
	}
	wantPrimaryOID, err := primaryExec.Run(ctx, ".", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	wantPrimaryOID = strings.TrimSpace(wantPrimaryOID)

	secViewID := privategit.SecondaryViewID(viewA.ViewID, "second")
	_, _ = viewA.Core.WriteFile(layeredfs.QualifiedPath{RootID: "second", Rel: "sec.txt"}, []byte("sec-v1\n"), 0o644)
	secExec := privategit.NewGitExecutor(fx.gitMgr, secViewID, privategit.RemotePolicy{})
	if _, err := secExec.Run(ctx, ".", "checkout", "-b", "feat-second"); err != nil {
		t.Fatalf("second checkout: %v", err)
	}
	if _, err := secExec.Run(ctx, ".", "add", "sec.txt"); err != nil {
		t.Fatalf("second add: %v", err)
	}
	if _, err := secExec.Commit(ctx, "second commit"); err != nil {
		t.Fatalf("second commit: %v", err)
	}
	wantSecondOID, err := secExec.Run(ctx, ".", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	wantSecondOID = strings.TrimSpace(wantSecondOID)

	cpGit, err := fx.gitMgr.CaptureCheckpointGitState(ctx, viewA.ViewID, "cp-multi-git")
	if err != nil {
		t.Fatalf("CaptureCheckpointGitState primary: %v", err)
	}
	cpSecGit, err := fx.gitMgr.CaptureCheckpointGitState(ctx, secViewID, "cp-multi-git-second")
	if err != nil {
		t.Fatalf("CaptureCheckpointGitState second: %v", err)
	}
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:             viewA.Core,
		Store:            fx.cpStore,
		WorkerID:         workerID,
		TaskID:           "task-multi-git",
		ProjectID:        "proj-multi-git",
		SourceGeneration: "gen-1",
		SelectedRoots: []layeredfs.RootSpec{
			{ID: "primary", Label: "primary", TrunkPath: fx.trunkDir, IsPrimary: true},
			{ID: "second", Label: "second", TrunkPath: trunkSecond, IsPrimary: false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := pub.Publish(ctx, checkpoint.PublishRequest{
		CheckpointID: "cp-multi-git",
		PrivateGit: checkpoint.PrivateGitSnapshot{
			HeadRef:         cpGit.HeadSymbolic,
			Refs:            cpGit.Refs,
			RetainedObjects: append(append([]string(nil), cpGit.Artifact.ReachableOIDs...), cpSecGit.Artifact.ReachableOIDs...),
			Roots: map[string]checkpoint.RootGitRefs{
				"second": {
					HeadRef: cpSecGit.HeadSymbolic,
					Refs:    cpSecGit.Refs,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Prepare a fresh view without any pre-initialized private git repos and restore the checkpoint into it.
	viewRec, err := fx.mountedAdp.Prepare(ctx, multiPrepareReq("view-multi-rec"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(context.Background(), viewRec.ViewID) }()
	if err := fx.mountedAdp.BindPrivateGit(viewRec.ViewID, workerID+"-rec", fx.gitMgr); err != nil {
		t.Fatalf("BindPrivateGit viewRec: %v", err)
	}
	if err := fx.coord.ApplyCheckpointToView(ctx, viewRec, manifest); err != nil {
		t.Fatalf("ApplyCheckpointToView: %v", err)
	}

	recPrimaryExec := privategit.NewGitExecutor(fx.gitMgr, viewRec.ViewID, privategit.RemotePolicy{})
	gotPrimaryOID, err := recPrimaryExec.Run(ctx, ".", "rev-parse", "refs/heads/feat-primary")
	if err != nil || strings.TrimSpace(gotPrimaryOID) != wantPrimaryOID {
		t.Fatalf("restored primary ref = %q (err=%v), want %s", strings.TrimSpace(gotPrimaryOID), err, wantPrimaryOID)
	}

	recSecExec := privategit.NewGitExecutor(fx.gitMgr, privategit.SecondaryViewID(viewRec.ViewID, "second"), privategit.RemotePolicy{})
	gotSecondOID, err := recSecExec.Run(ctx, ".", "rev-parse", "refs/heads/feat-second")
	if err != nil || strings.TrimSpace(gotSecondOID) != wantSecondOID {
		t.Fatalf("restored secondary ref = %q (err=%v), want %s", strings.TrimSpace(gotSecondOID), err, wantSecondOID)
	}

	// Also verify materialized view reconstruction when AdoptMaterializedView registered the view
	// without pre-initialized Repos (so restoreRootGitRefs initializes via InitRepo on demand).
	eagerAdp := adapter.NewEagerCopyAdapter(adapter.ReasonNone)
	viewMat, err := eagerAdp.Prepare(ctx, multiPrepareReq("view-multi-mat"))
	if err != nil {
		t.Fatalf("eagerAdp.Prepare: %v", err)
	}
	defer func() { _, _ = eagerAdp.Release(context.Background(), viewMat.ViewID) }()
	var matPrimaryWT, matSecondWT string
	for _, r := range viewMat.Roots {
		switch r.RootID {
		case "primary":
			matPrimaryWT = r.WorkspacePath
		case "second":
			matSecondWT = r.WorkspacePath
		}
	}
	if err := fx.gitMgr.AdoptMaterializedView(ctx, privategit.MaterializedViewBinding{
		ViewID:      viewMat.ViewID,
		WorkerID:    workerID + "-mat",
		RootID:      "primary",
		TrunkDir:    fx.trunkDir,
		WorktreeDir: matPrimaryWT,
		Core:        viewMat.Core,
	}); err != nil {
		t.Fatalf("AdoptMaterializedView primary: %v", err)
	}
	if err := fx.gitMgr.AdoptMaterializedView(ctx, privategit.MaterializedViewBinding{
		ViewID:      privategit.SecondaryViewID(viewMat.ViewID, "second"),
		WorkerID:    workerID + "-mat",
		RootID:      "second",
		TrunkDir:    trunkSecond,
		WorktreeDir: matSecondWT,
		Core:        viewMat.Core,
	}); err != nil {
		t.Fatalf("AdoptMaterializedView second: %v", err)
	}
	if err := fx.coord.ApplyCheckpointToView(ctx, viewMat, manifest); err != nil {
		t.Fatalf("ApplyCheckpointToView materialized: %v", err)
	}
	matPrimaryExec := privategit.NewGitExecutor(fx.gitMgr, viewMat.ViewID, privategit.RemotePolicy{})
	gotMatPrimaryOID, err := matPrimaryExec.Run(ctx, ".", "rev-parse", "refs/heads/feat-primary")
	if err != nil || strings.TrimSpace(gotMatPrimaryOID) != wantPrimaryOID {
		t.Fatalf("materialized restored primary ref = %q (err=%v), want %s", strings.TrimSpace(gotMatPrimaryOID), err, wantPrimaryOID)
	}
	matSecExec := privategit.NewGitExecutor(fx.gitMgr, privategit.SecondaryViewID(viewMat.ViewID, "second"), privategit.RemotePolicy{})
	gotMatSecondOID, err := matSecExec.Run(ctx, ".", "rev-parse", "refs/heads/feat-second")
	if err != nil || strings.TrimSpace(gotMatSecondOID) != wantSecondOID {
		t.Fatalf("materialized restored secondary ref = %q (err=%v), want %s", strings.TrimSpace(gotMatSecondOID), err, wantSecondOID)
	}
}
