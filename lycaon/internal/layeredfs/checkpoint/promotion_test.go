package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExactBaseScenariosAndThreeWayPromotion(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	storeDir := filepath.Join(tmp, "store")
	for _, d := range []string{trunk, backing} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(d, 0o755))
	}

	// Seed trunk fixtures for append, truncate, rename-over, delete, hard links, and dir replacement.
	testutil.FailErr(t, "seed append.txt", os.WriteFile(filepath.Join(trunk, "append.txt"), []byte("header-orig\nshared-middle\nfooter-orig\n"), 0o644))
	testutil.FailErr(t, "seed trunc.txt", os.WriteFile(filepath.Join(trunk, "trunc.txt"), []byte("0123456789"), 0o644))
	testutil.FailErr(t, "seed sed.txt", os.WriteFile(filepath.Join(trunk, "sed.txt"), []byte("before-sed\n"), 0o644))
	testutil.FailErr(t, "seed delete_me.txt", os.WriteFile(filepath.Join(trunk, "delete_me.txt"), []byte("remove-me"), 0o644))
	testutil.FailErr(t, "seed hard_a.txt", os.WriteFile(filepath.Join(trunk, "hard_a.txt"), []byte("link-seed"), 0o644))
	testutil.FailErr(t, "seed file_to_dir", os.WriteFile(filepath.Join(trunk, "file_to_dir"), []byte("was-a-file"), 0o644))

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:     "view-promo",
		BackingDir: backing,
		Roots:      []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	qp := func(rel string) layeredfs.QualifiedPath {
		return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
	}

	// 1. Worker edits footer of append.txt while source concurrently edits header.
	_, err = core.WriteFile(qp("append.txt"), []byte("header-orig\nshared-middle\nfooter-worker-appended\n"), 0o644)
	testutil.FailErr(t, "worker edit append.txt", err)
	testutil.FailErr(t, "concurrent source edit append.txt", os.WriteFile(filepath.Join(trunk, "append.txt"), []byte("header-source-updated\nshared-middle\nfooter-orig\n"), 0o644))

	// 2. Truncate.
	_, err = core.Truncate(qp("trunc.txt"), 4)
	testutil.FailErr(t, "truncate trunc.txt", err)

	// 3. Rename-over (sed -i style).
	_, err = core.WriteFile(qp(".sed.tmp"), []byte("after-sed-rename-over\n"), 0o644)
	testutil.FailErr(t, "write .sed.tmp", err)
	testutil.FailErr(t, "rename over sed.txt", core.Rename(qp(".sed.tmp"), qp("sed.txt")))

	// 4. Delete.
	testutil.FailErr(t, "unlink delete_me.txt", core.Unlink(qp("delete_me.txt"), false))

	// 5. Hard links.
	_, err = core.Link(qp("hard_a.txt"), qp("hard_b.txt"))
	testutil.FailErr(t, "link hard_a -> hard_b", err)
	_, err = core.WriteFile(qp("hard_b.txt"), []byte("linked-updated-bytes"), 0o644)
	testutil.FailErr(t, "write hard_b.txt", err)

	// 6. Directory replacement (file -> directory + child).
	testutil.FailErr(t, "unlink file_to_dir", core.Unlink(qp("file_to_dir"), false))
	_, err = core.Mkdir(qp("file_to_dir"), 0o755)
	testutil.FailErr(t, "mkdir file_to_dir", err)
	_, err = core.WriteFile(qp("file_to_dir/child.txt"), []byte("inside-replaced-dir"), 0o644)
	testutil.FailErr(t, "write file_to_dir/child.txt", err)

	store, err := NewStore(storeDir)
	testutil.FailErr(t, "NewStore", err)
	pub, err := NewPublisher(PublisherConfig{
		Core:             core,
		Store:            store,
		WorkerID:         "w-1",
		TaskID:           "t-1",
		ProjectID:        "p-1",
		SourceGeneration: "gen-1",
	})
	testutil.FailErr(t, "NewPublisher", err)

	pubRes, err := pub.Publish(context.Background(), PublishRequest{
		CheckpointID: "chk-scenarios",
		Trigger:      TriggerWorkerCompletion,
	})
	testutil.FailErr(t, "Publish chk-scenarios", err)

	promoter, err := NewPromoter(PromoterConfig{
		Store:       store,
		TargetRoots: map[string]string{"primary": trunk},
	})
	testutil.FailErr(t, "NewPromoter", err)

	preview1, err := promoter.Preview(context.Background(), PreviewRequest{
		CheckpointID:             pubRes.Manifest.CheckpointID,
		ExpectedWorkerID:         "w-1",
		ExpectedTaskID:           "t-1",
		ExpectedProjectID:        "p-1",
		ExpectedSourceGeneration: "gen-1",
	})
	testutil.FailErr(t, "Preview 1", err)
	if len(preview1.Conflicts) != 0 {
		t.Fatalf("expected 0 conflicts, got %+v", preview1.Conflicts)
	}

	// Worker continues editing mutable view after checkpoint; Preview must remain byte-identical!
	_, err = core.WriteFile(qp("sed.txt"), []byte("dirty-uncheckpointed-edit\n"), 0o644)
	testutil.FailErr(t, "worker post-checkpoint edit", err)
	preview2, err := promoter.Preview(context.Background(), PreviewRequest{
		CheckpointID: pubRes.Manifest.CheckpointID,
	})
	testutil.FailErr(t, "Preview 2", err)
	for i := range preview1.PlannedChanges {
		if !bytes.Equal(preview1.PlannedChanges[i].MergedBytes, preview2.PlannedChanges[i].MergedBytes) {
			t.Fatalf("preview mutated while worker edited working tree at index %d", i)
		}
	}

	// Apply preview and verify target state.
	rec, err := promoteAndCommit(t, promoter, ApplyRequest{
		TransactionID: "tx-scenarios",
		Preview:       preview1,
	})
	testutil.FailErr(t, "Apply scenarios", err)
	if rec.State != PromotionStateApplied || rec.FilePromotionStatus != EffectCompleted {
		t.Fatalf("unexpected apply record: %+v", rec)
	}

	gotAppend, err := os.ReadFile(filepath.Join(trunk, "append.txt"))
	testutil.FailErr(t, "read promoted append.txt", err)
	if string(gotAppend) != "header-source-updated\nshared-middle\nfooter-worker-appended\n" {
		t.Fatalf("unexpected 3-way merged append.txt: %q", string(gotAppend))
	}
	gotTrunc, err := os.ReadFile(filepath.Join(trunk, "trunc.txt"))
	testutil.FailErr(t, "read promoted trunc.txt", err)
	if string(gotTrunc) != "0123" {
		t.Fatalf("unexpected promoted trunc.txt: %q", string(gotTrunc))
	}
	if _, err := os.Stat(filepath.Join(trunk, "delete_me.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected delete_me.txt to be removed, got %v", err)
	}
	gotChild, err := os.ReadFile(filepath.Join(trunk, "file_to_dir", "child.txt"))
	testutil.FailErr(t, "read file_to_dir/child.txt", err)
	if string(gotChild) != "inside-replaced-dir" {
		t.Fatalf("unexpected child content: %q", string(gotChild))
	}
	infoA, err := os.Stat(filepath.Join(trunk, "hard_a.txt"))
	testutil.FailErr(t, "stat hard_a.txt", err)
	infoB, err := os.Stat(filepath.Join(trunk, "hard_b.txt"))
	testutil.FailErr(t, "stat hard_b.txt", err)
	stA, okA := infoA.Sys().(*syscall.Stat_t)
	stB, okB := infoB.Sys().(*syscall.Stat_t)
	if okA && okB && stA.Ino != stB.Ino {
		t.Fatalf("expected promoted hard_a.txt and hard_b.txt to share inode, got %d vs %d", stA.Ino, stB.Ino)
	}
}

func TestAmbiguousCandidateBasesStaleTargetAndRetryIdempotency(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	storeDir := filepath.Join(tmp, "store")
	for _, d := range []string{trunk, backing} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(d, 0o755))
	}

	targetContent := []byte("line1-A\nanchor\nline3-base\n")
	workerContent := []byte("line1-B\nanchor\nline3-worker\n")
	cand1Bytes := []byte("line1-A\nanchor\nline3-base\n")
	cand2Bytes := []byte("line1-B\nanchor\nline3-base\n")
	testutil.FailErr(t, "seed ambig.txt", os.WriteFile(filepath.Join(trunk, "ambig.txt"), targetContent, 0o644))

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:     "view-ambig",
		BackingDir: backing,
		Roots:      []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	qp := layeredfs.QualifiedPath{RootID: "primary", Rel: "ambig.txt"}
	_, err = core.WriteFile(qp, workerContent, 0o644)
	testutil.FailErr(t, "write worker ambig.txt", err)

	store, err := NewStore(storeDir)
	testutil.FailErr(t, "NewStore", err)
	sha1, _, err := store.WriteBlobBytes(cand1Bytes)
	testutil.FailErr(t, "write cand1 blob", err)
	sha2, _, err := store.WriteBlobBytes(cand2Bytes)
	testutil.FailErr(t, "write cand2 blob", err)

	pub, err := NewPublisher(PublisherConfig{
		Core:             core,
		Store:            store,
		WorkerID:         "w-ambig",
		TaskID:           "t-ambig",
		ProjectID:        "p-ambig",
		SourceGeneration: "gen-1",
	})
	testutil.FailErr(t, "NewPublisher", err)

	_, err = pub.Publish(context.Background(), PublishRequest{
		CheckpointID: "chk-ambig",
		Trigger:      TriggerExplicitCapture,
		PrivateGit: PrivateGitSnapshot{
			HeadRef: "refs/heads/main",
			Refs:    map[string]string{"refs/heads/main": "deadbeef"},
		},
		CandidateBases: map[layeredfs.QualifiedPath][]layeredfs.BaseCapture{
			qp: {
				{Path: qp, Kind: layeredfs.EntryRegular, Size: int64(len(cand1Bytes)), SHA256: sha1},
				{Path: qp, Kind: layeredfs.EntryRegular, Size: int64(len(cand2Bytes)), SHA256: sha2},
			},
		},
	})
	testutil.FailErr(t, "Publish chk-ambig", err)

	promoter, err := NewPromoter(PromoterConfig{
		Store:       store,
		TargetRoots: map[string]string{"primary": trunk},
	})
	testutil.FailErr(t, "NewPromoter", err)

	// 1. Two plausible candidate bases that both merge cleanly but produce different outputs MUST conflict!
	prevConflict, err := promoter.Preview(context.Background(), PreviewRequest{CheckpointID: "chk-ambig"})
	testutil.FailErr(t, "Preview conflict", err)
	if len(prevConflict.Conflicts) != 1 || prevConflict.Conflicts[0].Kind != ConflictAmbiguousBase || len(prevConflict.Conflicts[0].AmbiguousOutputs) != 2 {
		t.Fatalf("expected ConflictAmbiguousBase with 2 distinct outputs, got %+v", prevConflict.Conflicts)
	}

	// 2. Resolve conflict manually, then mutate target before Apply -> ErrStalePreviewTargetChanged.
	resolvedBytes := []byte("line1-resolved\nanchor\nline3-worker\n")
	prevResolved, err := promoter.Preview(context.Background(), PreviewRequest{
		CheckpointID: "chk-ambig",
		Resolutions: map[layeredfs.QualifiedPath]ConflictResolution{
			qp: {Path: qp, Choice: ResolutionManualBytes, ManualBytes: resolvedBytes},
		},
	})
	testutil.FailErr(t, "Preview resolved", err)

	testutil.FailErr(t, "mutate target behind preview", os.WriteFile(filepath.Join(trunk, "ambig.txt"), []byte("concurrent-target-change\n"), 0o644))
	if _, err := promoteAndCommit(t, promoter, ApplyRequest{Preview: prevResolved}); !errors.Is(err, ErrStalePreviewTargetChanged) {
		t.Fatalf("expected ErrStalePreviewTargetChanged, got %v", err)
	}

	// 3. Renew preview on updated target. A fault before ref import restores the target, and a retry applies once.
	prevRenewed, err := promoter.Preview(context.Background(), PreviewRequest{
		CheckpointID: "chk-ambig",
		Resolutions: map[layeredfs.QualifiedPath]ConflictResolution{
			qp: {Path: qp, Choice: ResolutionManualBytes, ManualBytes: resolvedBytes},
		},
	})
	testutil.FailErr(t, "Preview renewed", err)

	interrupted := errors.New("interrupted before ref import")
	_, err = promoteAndCommit(t, promoter, ApplyRequest{
		TransactionID: "tx-retry-1",
		Preview:       prevRenewed,
		FaultInjector: func(stage ApplyFaultStage) error {
			if stage == ApplyFaultAfterFilesBeforeRefs {
				return interrupted
			}
			return nil
		},
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("expected interruption, got %v", err)
	}
	restored, err := os.ReadFile(filepath.Join(trunk, "ambig.txt"))
	testutil.FailErr(t, "read restored target", err)
	if string(restored) != "concurrent-target-change\n" {
		t.Fatalf("failed promotion left target %q", restored)
	}
	if _, found, err := store.LoadPromotionRecord("tx-retry-1"); err != nil || found {
		t.Fatalf("failed promotion left a record: found=%v err=%v", found, err)
	}

	rec, err := promoteAndCommit(t, promoter, ApplyRequest{TransactionID: "tx-retry-1", Preview: prevRenewed})
	testutil.FailErr(t, "Apply retry", err)
	if rec.State != PromotionStateApplied {
		t.Fatalf("unexpected retry state: rec=%+v", rec)
	}

	// A recorded promotion is a complete no-op, whatever the target holds now.
	testutil.FailErr(t, "edit after landing", os.WriteFile(filepath.Join(trunk, "ambig.txt"), []byte("later\n"), 0o644))
	_, err = promoteAndCommit(t, promoter, ApplyRequest{TransactionID: "tx-retry-1", Preview: prevRenewed})
	testutil.FailErr(t, "Apply idempotent second retry", err)
	after, err := os.ReadFile(filepath.Join(trunk, "ambig.txt"))
	testutil.FailErr(t, "read after idempotent retry", err)
	if string(after) != "later\n" {
		t.Fatalf("idempotent retry changed target: %q", after)
	}

	// Rejected checkpoints remain available in the store.
	rejRec, err := promoter.Reject(context.Background(), "chk-ambig", "superseded by manual review")
	testutil.FailErr(t, "Reject checkpoint", err)
	if rejRec.State != PromotionStateRejected {
		t.Fatalf("expected rejected state, got %+v", rejRec)
	}
	gotStored, err := store.ReadEntryContent("chk-ambig", qp)
	testutil.FailErr(t, "ReadEntryContent after rejection", err)
	if !bytes.Equal(gotStored, workerContent) {
		t.Fatalf("expected rejected checkpoint content to remain readable, got %q", string(gotStored))
	}
}
