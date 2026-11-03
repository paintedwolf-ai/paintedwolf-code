package recovery_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
)

func TestStackedWorkerParentContinuationEvictionAndBothPromotionOrders(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := t.Context()

	// 1. Parent worker modifies app.go, creates parent_only.txt, deletes sub/dir.txt, and publishes cp-parent-A.
	parentView, err := fx.mountedAdp.Prepare(ctx, fx.prepareRequest("view-parent"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(ctx, parentView.ViewID) }()

	qpApp := layeredfs.QualifiedPath{RootID: "primary", Rel: "app.go"}
	qpParentOnly := layeredfs.QualifiedPath{RootID: "primary", Rel: "parent_only.txt"}
	qpSubDir := layeredfs.QualifiedPath{RootID: "primary", Rel: "sub/dir.txt"}
	qpChildOnly := layeredfs.QualifiedPath{RootID: "primary", Rel: "child_only.txt"}

	_, _ = parentView.Core.WriteFile(qpApp, []byte("package main // v1-parent\n"), 0o644)
	_, _ = parentView.Core.WriteFile(qpParentOnly, []byte("parent-secret\n"), 0o644)
	_ = parentView.Core.Unlink(qpSubDir, false)

	parentPub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:             parentView.Core,
		Store:            fx.cpStore,
		WorkerID:         "worker-parent",
		TaskID:           "task-stack",
		ProjectID:        "proj-stack",
		SourceGeneration: "gen-1",
		SelectedRoots:    []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: fx.trunkDir, IsPrimary: true}},
		Callbacks: checkpoint.BarrierCallbacks{
			CommitRetention: func(cCtx context.Context, cpID string, blobs, gitObjs []string) error {
				return fx.coord.CommitCheckpointRetention(cCtx, "worker-parent", cpID, blobs, gitObjs, nil)
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parentPub.Publish(ctx, checkpoint.PublishRequest{CheckpointID: "cp-parent-A"}); err != nil {
		t.Fatal(err)
	}
	_ = fx.coord.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
		FormatVersion:             recovery.FormatVersion,
		WorkerID:                  "worker-parent",
		ViewID:                    parentView.ViewID,
		TaskID:                    "task-stack",
		ProjectID:                 "proj-stack",
		AdapterID:                 fx.mountedAdp.ID(),
		LastPublishedCheckpointID: "cp-parent-A",
	})

	// 2. Prepare stacked child worker over cp-parent-A.
	stackedSess, err := fx.coord.PrepareStackedWorkerView(ctx, recovery.StackedViewRequest{
		ChildWorkerID:      "worker-child",
		ChildViewID:        "view-child",
		TaskID:             "task-stack",
		ProjectID:          "proj-stack",
		ParentCheckpointID: "cp-parent-A",
		Adapter:            fx.mountedAdp,
		PrepareReq:         fx.prepareRequest("view-child"),
	})
	if err != nil {
		t.Fatalf("PrepareStackedWorkerView: %v", err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(ctx, stackedSess.ChildViewID) }()

	// 3. Parent continues mutating its own working view and attempts to evict/GC cp-parent-A.
	_, _ = parentView.Core.WriteFile(qpApp, []byte("package main // parent-diverged-later\n"), 0o644)
	_ = parentView.Core.Unlink(qpParentOnly, false)

	if err := fx.coord.EvictCheckpoint(ctx, "cp-parent-A"); !errors.Is(err, recovery.ErrEvictReferencedCheckpoint) {
		t.Fatalf("expected ErrEvictReferencedCheckpoint while stacked child pins parent, got %v", err)
	}
	gcRep, err := fx.coord.CollectGarbage(ctx)
	if err != nil || !slices.Contains(gcRep.RetainedCheckpoints, "cp-parent-A") {
		t.Fatalf("expected cp-parent-A retained by GC, got %+v err=%v", gcRep, err)
	}

	// Verify child still reads immutable cp-parent-A over live trunk.
	childCore := stackedSess.PreparedView.Core
	if got, rErr := childCore.ReadFile(qpApp); rErr != nil || !bytes.Equal(got, []byte("package main // v1-parent\n")) {
		t.Fatalf("child app.go mutated by parent continuation: got %q err=%v", string(got), rErr)
	}
	if got, rErr := childCore.ReadFile(qpParentOnly); rErr != nil || !bytes.Equal(got, []byte("parent-secret\n")) {
		t.Fatalf("child parent_only.txt missing: got %q err=%v", string(got), rErr)
	}
	if _, sErr := childCore.Lstat(qpSubDir); !errors.Is(sErr, os.ErrNotExist) {
		t.Fatalf("expected sub/dir.txt whiteout preserved in child, got %v", sErr)
	}

	// 4. Child modifies app.go, creates child_only.txt, and publishes cp-child-C.
	_, _ = childCore.WriteFile(qpApp, []byte("package main // v2-child\n"), 0o644)
	_, _ = childCore.WriteFile(qpChildOnly, []byte("child-feature\n"), 0o644)

	childPub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:              childCore,
		Store:             fx.cpStore,
		WorkerID:          "worker-child",
		TaskID:            "task-stack",
		ProjectID:         "proj-stack",
		SourceGeneration:  "gen-1",
		SelectedRoots:     []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: fx.trunkDir, IsPrimary: true}},
		ParentCheckpoints: stackedSess.ParentCheckpointIDs,
		Callbacks: checkpoint.BarrierCallbacks{
			CommitRetention: func(cCtx context.Context, cpID string, blobs, gitObjs []string) error {
				return fx.coord.CommitCheckpointRetention(cCtx, "worker-child", cpID, blobs, gitObjs, stackedSess.ParentCheckpointIDs)
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := childPub.Publish(ctx, checkpoint.PublishRequest{CheckpointID: "cp-child-C"}); err != nil {
		t.Fatal(err)
	}

	// 5. Verify both promotion orders produce identical, non-duplicated trunk state.
	verifyStackedPromotionOrders(t, fx)
}

func verifyStackedPromotionOrders(t *testing.T, fx *recoveryFixture) {
	t.Helper()
	ctx := t.Context()

	// Order 1: Child promotes directly before parent lands (even when trunk has matching prose).
	trunkDirect := filepath.Join(fx.baseDir, "trunk-direct")
	copyDirForTest(t, fx.trunkDir, trunkDirect)
	writeFile(t, filepath.Join(trunkDirect, "NOTES.md"), "Prose mentioning cp-parent-A and parent-secret without landing receipt\n")

	promoterDirect, err := checkpoint.NewPromoter(checkpoint.PromoterConfig{
		Store:       fx.cpStore,
		TargetRoots: map[string]string{"primary": trunkDirect},
	})
	if err != nil {
		t.Fatal(err)
	}
	planDirect, err := fx.coord.ResolveStackedPromotionPlan(ctx, recovery.StackedPromotionRequest{
		ChildCheckpointID: "cp-child-C",
		TargetRoots:       map[string]string{"primary": trunkDirect},
		Promoter:          promoterDirect,
	})
	if err != nil {
		t.Fatalf("ResolveStackedPromotionPlan (direct): %v", err)
	}
	if !slices.Equal(planDirect.UnlandedAncestors, []string{"cp-parent-A"}) || len(planDirect.LandedAncestors) != 0 {
		t.Fatalf("expected cp-parent-A unlanded in direct order, got landed=%v unlanded=%v", planDirect.LandedAncestors, planDirect.UnlandedAncestors)
	}
	if _, err := fx.coord.PromoteStackedCheckpoint(ctx, recovery.StackedPromotionRequest{
		ChildCheckpointID: "cp-child-C",
		TargetRoots:       map[string]string{"primary": trunkDirect},
		Promoter:          promoterDirect,
		TransactionID:     "tx-direct-child-C",
	}); err != nil {
		t.Fatalf("PromoteStackedCheckpoint (direct): %v", err)
	}
	assertExpectedStackedTrunk(t, trunkDirect)

	// Order 2: Parent promotes first (recording LandingReceipt), then Child promotes.
	trunkSeq := filepath.Join(fx.baseDir, "trunk-seq")
	copyDirForTest(t, fx.trunkDir, trunkSeq)
	promoterSeq, err := checkpoint.NewPromoter(checkpoint.PromoterConfig{
		Store:       fx.cpStore,
		TargetRoots: map[string]string{"primary": trunkSeq},
	})
	if err != nil {
		t.Fatal(err)
	}
	prevParent, err := promoterSeq.Preview(ctx, checkpoint.PreviewRequest{CheckpointID: "cp-parent-A"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoterSeq.Apply(ctx, checkpoint.ApplyRequest{TransactionID: "tx-cp-parent-A", Preview: prevParent}); err != nil {
		t.Fatal(err)
	}
	_ = fx.coord.RecordCheckpointLanded(recovery.LandingReceipt{
		CheckpointID:  "cp-parent-A",
		TransactionID: "tx-cp-parent-A",
		WorkerID:      "worker-parent",
		ProjectID:     "proj-stack",
	})

	planSeq, err := fx.coord.ResolveStackedPromotionPlan(ctx, recovery.StackedPromotionRequest{
		ChildCheckpointID: "cp-child-C",
		TargetRoots:       map[string]string{"primary": trunkSeq},
		Promoter:          promoterSeq,
	})
	if err != nil {
		t.Fatalf("ResolveStackedPromotionPlan (parent-then-child): %v", err)
	}
	if !slices.Equal(planSeq.LandedAncestors, []string{"cp-parent-A"}) {
		t.Fatalf("expected cp-parent-A recognized as landed, got landed=%v unlanded=%v", planSeq.LandedAncestors, planSeq.UnlandedAncestors)
	}
	if _, err := fx.coord.PromoteStackedCheckpoint(ctx, recovery.StackedPromotionRequest{
		ChildCheckpointID: "cp-child-C",
		TargetRoots:       map[string]string{"primary": trunkSeq},
		Promoter:          promoterSeq,
		TransactionID:     "tx-seq-child-C",
	}); err != nil {
		t.Fatalf("PromoteStackedCheckpoint (parent-then-child): %v", err)
	}
	assertExpectedStackedTrunk(t, trunkSeq)
}

func assertExpectedStackedTrunk(t *testing.T, rootDir string) {
	t.Helper()
	gotApp, err := os.ReadFile(filepath.Join(rootDir, "app.go"))
	if err != nil || string(gotApp) != "package main // v2-child\n" {
		t.Fatalf("unexpected app.go in %s: %q err=%v", rootDir, string(gotApp), err)
	}
	gotParent, err := os.ReadFile(filepath.Join(rootDir, "parent_only.txt"))
	if err != nil || string(gotParent) != "parent-secret\n" {
		t.Fatalf("unexpected parent_only.txt in %s: %q err=%v", rootDir, string(gotParent), err)
	}
	gotChild, err := os.ReadFile(filepath.Join(rootDir, "child_only.txt"))
	if err != nil || string(gotChild) != "child-feature\n" {
		t.Fatalf("unexpected child_only.txt in %s: %q err=%v", rootDir, string(gotChild), err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "sub", "dir.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected sub/dir.txt deleted in %s, got err=%v", rootDir, err)
	}
}

func TestEvictDisposableWorkingMaterialAndGarbageCollection(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := t.Context()

	view, err := fx.mountedAdp.Prepare(ctx, fx.prepareRequest("view-evict"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(ctx, view.ViewID) }()

	qpApp := layeredfs.QualifiedPath{RootID: "primary", Rel: "app.go"}
	_, _ = view.Core.WriteFile(qpApp, []byte("package main // evict-v1\n"), 0o644)

	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:             view.Core,
		Store:            fx.cpStore,
		WorkerID:         "worker-evict",
		TaskID:           "t-ev",
		ProjectID:        "p-ev",
		SourceGeneration: "g-ev",
		SelectedRoots:    []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: fx.trunkDir, IsPrimary: true}},
		Callbacks: checkpoint.BarrierCallbacks{
			CommitRetention: func(cCtx context.Context, cpID string, blobs, gitObjs []string) error {
				return fx.coord.CommitCheckpointRetention(cCtx, "worker-evict", cpID, blobs, gitObjs, nil)
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pubRes, err := pub.Publish(ctx, checkpoint.PublishRequest{CheckpointID: "cp-evict-1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = fx.coord.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
		FormatVersion:             recovery.FormatVersion,
		WorkerID:                  "worker-evict",
		ViewID:                    view.ViewID,
		TaskID:                    "t-ev",
		ProjectID:                 "p-ev",
		AdapterID:                 fx.mountedAdp.ID(),
		LastPublishedCheckpointID: "cp-evict-1",
	})

	// Write unsealed mutation: EvictDisposableWorkingMaterial must refuse.
	_, _ = view.Core.WriteFile(qpApp, []byte("package main // evict-v2-unsealed\n"), 0o644)
	if _, err := fx.coord.EvictDisposableWorkingMaterial(ctx, "worker-evict", view.ViewID, view.BackingDir); !errors.Is(err, recovery.ErrEvictUnsealedWorkRefused) {
		t.Fatalf("expected ErrEvictUnsealedWorkRefused for unsealed mutation, got %v", err)
	}

	// Publish cp-evict-2 to seal the mutation, then EvictDisposableWorkingMaterial must succeed.
	if _, err := pub.Publish(ctx, checkpoint.PublishRequest{CheckpointID: "cp-evict-2"}); err != nil {
		t.Fatal(err)
	}
	_ = fx.coord.RecordTransition("worker-evict", view.ViewID, recovery.PhasePublication, recovery.StateCommitted, "cp-evict-2", "publish", nil, false)

	evRep, err := fx.coord.EvictDisposableWorkingMaterial(ctx, "worker-evict", view.ViewID, view.BackingDir)
	if err != nil || evRep.ReclaimedBytes <= 0 {
		t.Fatalf("expected EvictDisposableWorkingMaterial to reclaim bytes, got %+v err=%v", evRep, err)
	}

	// Write an unreferenced blob and an orphaned staged checkpoint; GC must collect them while keeping cp-evict-1 and cp-evict-2 blobs.
	orphanSHA, _, err := fx.cpStore.WriteBlobBytes([]byte("orphan-unreferenced-blob-payload\n"))
	if err != nil {
		t.Fatal(err)
	}
	orphanCP := filepath.Join(fx.cpStore.RootDir(), "checkpoints", "cp-orphan-tmp")
	_ = os.MkdirAll(orphanCP, 0o700)
	writeFile(t, filepath.Join(orphanCP, "manifest.json.tmp"), "{}")

	gcRep, err := fx.coord.CollectGarbage(ctx)
	if err != nil {
		t.Fatalf("CollectGarbage: %v", err)
	}
	if !slices.Contains(gcRep.CollectedBlobs, orphanSHA) || !slices.Contains(gcRep.CollectedStagedCheckpoints, "cp-orphan-tmp") {
		t.Fatalf("expected orphan blob and staged checkpoint collected, got %+v", gcRep)
	}
	for _, e := range pubRes.Manifest.Entries {
		if e.Final.SHA256 != "" && !slices.Contains(gcRep.RetainedBlobs, e.Final.SHA256) {
			t.Fatalf("expected referenced blob %s retained", e.Final.SHA256)
		}
	}
}

func copyDirForTest(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
}
