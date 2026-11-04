package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestApplyHunkResolutionsRejectsInvalidRangesWithoutMutation(t *testing.T) {
	original := []byte("one\ntwo\n")
	for _, hunks := range [][]api.WorkerPromoteHunkResolution{
		{{StartLine: 0, EndLine: 1, Content: "bad"}},
		{{StartLine: 5, EndLine: 5, Content: "bad"}},
		{{StartLine: 2, EndLine: 5, Content: "bad"}},
		{{StartLine: 3, EndLine: 3, Content: "append is not a replacement"}},
		{
			{StartLine: 1, EndLine: 2, Content: "first"},
			{StartLine: 2, EndLine: 2, Content: "overlap"},
		},
	} {
		if _, err := applyHunkResolutionsToBytes(original, "a.txt", hunks); err == nil {
			t.Fatalf("hunks %+v unexpectedly accepted", hunks)
		}
	}
	if string(original) != "one\ntwo\n" {
		t.Fatalf("input mutated: %q", original)
	}
}

func TestApplyHunkResolutionsUsesOriginalCoordinates(t *testing.T) {
	got, err := applyHunkResolutionsToBytes([]byte("one\ntwo\nthree\nfour\n"), "a.txt", []api.WorkerPromoteHunkResolution{
		{StartLine: 4, EndLine: 4, Content: "FOUR\nFIVE"},
		{StartLine: 1, EndLine: 2, Content: "ONE"},
	})
	testutil.FailErr(t, "apply hunks", err)
	if string(got) != "ONE\nthree\nFOUR\nFIVE\n" {
		t.Fatalf("resolved content = %q", got)
	}
}

func TestPromoteTransactionRecoversCommittedTargets(t *testing.T) {
	root := t.TempDir()
	dataDir := t.TempDir()
	task := &api.WorkerTask{ID: "job-recover", WorkspacePath: root, WorkspaceRoot: filepath.Join(root, "branch")}
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(task.WorkspaceRoot, 0o755))
	primary := filepath.Join(root, "script.sh")
	alias := filepath.Join(root, "script_alias.sh")
	testutil.FailErr(t, "seed primary", os.WriteFile(primary, []byte("old\n"), 0o755))
	testutil.FailErr(t, "link alias", os.Link(primary, alias))
	heldFD, err := os.Open(primary)
	testutil.FailErr(t, "open held fd", err)
	defer func() { _ = heldFD.Close() }()
	origStat, err := heldFD.Stat()
	testutil.FailErr(t, "stat held fd", err)

	plans := []promoteMutation{{
		path: "script.sh", original: []byte("old\n"), originalExists: true, originalMode: 0o755,
		target: []byte("new\n"), targetExists: true,
	}}
	tx, err := beginPromoteTransaction(dataDir, task.ID, plans)
	testutil.FailErr(t, "begin transaction", err)
	promote := PromoteRootsForTask(task, nil)
	testutil.FailErr(t, "apply transaction", tx.apply(t.Context(), promote, task))

	found, err := recoverPromoteTransaction(t.Context(), dataDir, task.ID, promote, task)
	testutil.FailErr(t, "recover transaction", err)
	if !found {
		t.Fatal("transaction journal not found")
	}
	raw, err := os.ReadFile(primary)
	testutil.FailErr(t, "read recovered primary", err)
	if string(raw) != "old\n" {
		t.Fatalf("recovered primary = %q", raw)
	}
	info, err := os.Stat(primary)
	testutil.FailErr(t, "stat recovered primary", err)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("recovered mode = %o", info.Mode().Perm())
	}
	aliasInfo, err := os.Stat(alias)
	testutil.FailErr(t, "stat alias", err)
	if !os.SameFile(info, aliasInfo) || !os.SameFile(info, origStat) {
		t.Fatal("expected Stage-1 promoteTransaction recovery to restore exact pre-promotion inode and hard-link identity")
	}

	// Re-apply and verify strict journal decoding and refusal when retained slot is replaced or missing.
	tx2, err := beginPromoteTransaction(dataDir, task.ID, plans)
	testutil.FailErr(t, "begin tx2", err)
	testutil.FailErr(t, "apply tx2", tx2.apply(t.Context(), promote, task))
	journal := filepath.Join(promoteTransactionDir(dataDir, task.ID), "transaction.json")
	validJournal, err := os.ReadFile(journal)
	testutil.FailErr(t, "read valid journal", err)
	testutil.FailErr(t, "write unknown field journal", os.WriteFile(journal, []byte(`{"version":1,"id":"x","phase":"applying","unknown":true}`), 0o600))
	if _, err := recoverPromoteTransaction(t.Context(), dataDir, task.ID, promote, task); err == nil {
		t.Fatal("expected unknown JSON field in promote journal to be rejected")
	}
	testutil.FailErr(t, "restore valid journal", os.WriteFile(journal, validJournal, 0o600))

	slotPath := filepath.Join(promoteTransactionDir(dataDir, task.ID), "retained", "slot-000000")
	testutil.FailErr(t, "remove retained slot", os.Remove(slotPath))
	testutil.FailErr(t, "replace retained slot with fresh inode", os.WriteFile(slotPath, []byte("old\n"), 0o755))
	if _, err := recoverPromoteTransaction(t.Context(), dataDir, task.ID, promote, task); err == nil {
		t.Fatal("expected replaced retained slot to refuse recovery")
	}
	testutil.FailErr(t, "delete retained slot", os.Remove(slotPath))
	if _, err := recoverPromoteTransaction(t.Context(), dataDir, task.ID, promote, task); err == nil {
		t.Fatal("expected missing retained slot to refuse recovery")
	}
}

func TestPromoteTransactionRecoversUnstartedAndPartialRetentionPreparation(t *testing.T) {
	root := t.TempDir()
	dataDir := t.TempDir()
	task := &api.WorkerTask{ID: "job-unstarted-prep", WorkspacePath: root, WorkspaceRoot: filepath.Join(root, "branch")}
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(task.WorkspaceRoot, 0o755))
	pathA := filepath.Join(root, "a.sh")
	pathB := filepath.Join(root, "b.sh")
	testutil.FailErr(t, "seed a.sh", os.WriteFile(pathA, []byte("old-a\n"), 0o755))
	testutil.FailErr(t, "seed b.sh", os.WriteFile(pathB, []byte("old-b\n"), 0o640))
	fdA, err := os.Open(pathA)
	testutil.FailErr(t, "open a.sh", err)
	defer func() { _ = fdA.Close() }()
	statA, err := fdA.Stat()
	testutil.FailErr(t, "stat a.sh", err)
	fdB, err := os.Open(pathB)
	testutil.FailErr(t, "open b.sh", err)
	defer func() { _ = fdB.Close() }()
	statB, err := fdB.Stat()
	testutil.FailErr(t, "stat b.sh", err)

	plans := []promoteMutation{
		{path: "a.sh", original: []byte("old-a\n"), originalExists: true, originalMode: 0o755, target: []byte("new-a\n"), targetExists: true},
		{path: "b.sh", original: []byte("old-b\n"), originalExists: true, originalMode: 0o640, target: []byte("new-b\n"), targetExists: true},
	}
	promote := PromoteRootsForTask(task, nil)

	// Crash immediately after begin, before apply creates any retained original.
	_, err = beginPromoteTransaction(dataDir, task.ID, plans)
	testutil.FailErr(t, "begin tx1", err)
	found, err := recoverPromoteTransaction(t.Context(), dataDir, task.ID, promote, task)
	testutil.FailErr(t, "recover unstarted tx1", err)
	if !found {
		t.Fatal("expected unstarted tx1 journal to be found")
	}

	// Crash with one of two originals retained, before any primary mutation.
	tx2, err := beginPromoteTransactionWith(dataDir, task.ID, plans, promoteTransactionEffects{Reached: func(step checkpoint.TransactionStep, _ string) error {
		if step == checkpoint.StepOriginalRetained {
			panic(errPromoteCrash)
		}
		return nil
	}})
	testutil.FailErr(t, "begin tx2", err)
	crashDuring(t, func() { _ = tx2.apply(t.Context(), promote, task) })
	found, err = recoverPromoteTransaction(t.Context(), dataDir, task.ID, promote, task)
	testutil.FailErr(t, "recover partially retained tx2", err)
	if !found {
		t.Fatal("expected partially retained tx2 journal to be found")
	}
	if _, err := os.Stat(promoteTransactionDir(dataDir, task.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected partial retained dir removed with the journal, stat err = %v", err)
	}

	curA, err := os.Stat(pathA)
	testutil.FailErr(t, "stat a.sh after recovery", err)
	curB, err := os.Stat(pathB)
	testutil.FailErr(t, "stat b.sh after recovery", err)
	if !os.SameFile(curA, statA) || !os.SameFile(curB, statB) {
		t.Fatal("expected untouched primary inodes preserved across unstarted and partial-retention recovery")
	}
}

func TestPromoteTransactionRefusesToOverwriteDivergentPrimary(t *testing.T) {
	root := t.TempDir()
	dataDir := t.TempDir()
	task := &api.WorkerTask{ID: "job-diverged", WorkspacePath: root, WorkspaceRoot: filepath.Join(root, "branch")}
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(task.WorkspaceRoot, 0o755))
	primary := filepath.Join(root, "a.txt")
	testutil.FailErr(t, "seed primary", os.WriteFile(primary, []byte("old\n"), 0o644))
	plans := []promoteMutation{{
		path: "a.txt", original: []byte("old\n"), originalExists: true, originalMode: 0o644,
		target: []byte("new\n"), targetExists: true,
	}}
	tx, err := beginPromoteTransaction(dataDir, task.ID, plans)
	testutil.FailErr(t, "begin transaction", err)
	promote := PromoteRootsForTask(task, nil)
	testutil.FailErr(t, "apply transaction", tx.apply(t.Context(), promote, task))
	testutil.FailErr(t, "external edit", os.WriteFile(primary, []byte("external\n"), 0o644))

	if _, err = recoverPromoteTransaction(t.Context(), dataDir, task.ID, promote, task); err == nil {
		t.Fatal("recovery accepted a divergent primary")
	}
	raw, readErr := os.ReadFile(primary)
	testutil.FailErr(t, "read divergent primary", readErr)
	if string(raw) != "external\n" {
		t.Fatalf("divergent primary overwritten: %q", raw)
	}
}

func TestPromoteTransactionValidatesWholeReadSetBeforeFirstWrite(t *testing.T) {
	root := t.TempDir()
	dataDir := t.TempDir()
	task := &api.WorkerTask{ID: "job-stale-read", WorkspacePath: root, WorkspaceRoot: filepath.Join(root, "branch")}
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(task.WorkspaceRoot, 0o755))
	for _, path := range []string{"a.txt", "b.txt"} {
		testutil.FailErr(t, "seed "+path, os.WriteFile(filepath.Join(root, path), []byte("old\n"), 0o644))
	}
	plans := []promoteMutation{
		{path: "a.txt", original: []byte("old\n"), originalExists: true, originalMode: 0o644, target: []byte("new-a\n"), targetExists: true},
		{path: "b.txt", original: []byte("old\n"), originalExists: true, originalMode: 0o644, target: []byte("new-b\n"), targetExists: true},
	}
	tx, err := beginPromoteTransaction(dataDir, task.ID, plans)
	testutil.FailErr(t, "begin transaction", err)
	testutil.FailErr(t, "concurrent edit", os.WriteFile(filepath.Join(root, "b.txt"), []byte("external\n"), 0o644))

	if err := tx.apply(t.Context(), PromoteRootsForTask(task, nil), task); err == nil {
		t.Fatal("apply accepted a stale read set")
	}
	if _, err := os.Lstat(promoteTransactionDir(dataDir, task.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unstarted failure left a record: %v", err)
	}
	a, readErr := os.ReadFile(filepath.Join(root, "a.txt"))
	testutil.FailErr(t, "read untouched first path", readErr)
	if string(a) != "old\n" {
		t.Fatalf("first path mutated before read-set validation completed: %q", a)
	}
}

func TestMergeServiceCheckpointExactBaseConflictSurface(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "store.db")
	sqlStore := NewSQLStore(sqlDB)
	q := NewSQLQueue(sqlDB, 8)
	primary := t.TempDir()
	testdbseed.InsertSessionWithRoot(t, sqlDB, "parent-cp-conf", testdbseed.DefaultProjectID, primary)

	testutil.FailErr(t, "write overlap.txt", os.WriteFile(filepath.Join(primary, "overlap.txt"), []byte("line1\n"), 0o644))
	testutil.FailErr(t, "write ambig.txt", os.WriteFile(filepath.Join(primary, "ambig.txt"), []byte("v1\n"), 0o644))

	jobID, err := q.Enqueue(ctx, api.WorkerTask{
		ID:              "job-cp-conf",
		ParentSessionID: "parent-cp-conf",
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   primary,
		AgentType:       "implementer",
		Prompt:          "conf",
		Brief:           "Conflict check",
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite},
	})
	testutil.FailErr(t, "Enqueue", err)

	overlay := filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches", jobID)
	testutil.FailErr(t, "MkdirAll overlay", os.MkdirAll(overlay, 0o755))
	cpStore := openRecoveryCheckpointStore(t)

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:     "view-cp-conf",
		BackingDir: t.TempDir(),
		Roots:      []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	overlapLP := layeredfs.QualifiedPath{RootID: "primary", Rel: "overlap.txt"}
	_, err = core.WriteFile(overlapLP, []byte("worker-line1\n"), 0o644)
	testutil.FailErr(t, "write overlap.txt", err)
	testutil.FailErr(t, "trunk overlap edit", os.WriteFile(filepath.Join(primary, "overlap.txt"), []byte("trunk-line1\n"), 0o644))

	ambigLP := layeredfs.QualifiedPath{RootID: "primary", Rel: "ambig.txt"}
	_, err = core.WriteFile(ambigLP, []byte("worker-after-ambig\n"), 0o644)
	testutil.FailErr(t, "write ambig.txt", err)

	cand1SHA, _, err := cpStore.WriteBlobBytes([]byte("cand1\n"))
	testutil.FailErr(t, "WriteBlobBytes cand1", err)
	cand2SHA, _, err := cpStore.WriteBlobBytes([]byte("cand2\n"))
	testutil.FailErr(t, "WriteBlobBytes cand2", err)

	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		WorkerID:         jobID,
		TaskID:           jobID,
		ProjectID:        testdbseed.DefaultProjectID,
		SourceGeneration: "gen-1",
		Core:             core,
		Store:            cpStore,
	})
	testutil.FailErr(t, "NewPublisher", err)

	_, err = pub.Publish(ctx, checkpoint.PublishRequest{
		CheckpointID: "chk-conf-1",
		Trigger:      checkpoint.TriggerExplicitCapture,
		CandidateBases: map[layeredfs.QualifiedPath][]layeredfs.BaseCapture{
			ambigLP: {
				{Path: ambigLP, Kind: layeredfs.EntryRegular, Size: 6, SHA256: cand1SHA},
				{Path: ambigLP, Kind: layeredfs.EntryRegular, Size: 6, SHA256: cand2SHA},
			},
		},
	})
	testutil.FailErr(t, "Publish", err)
	bindRecoveryCheckpoint(t, overlay, cpStore, "chk-conf-1")

	_, err = sqlStore.SetWorkerWorkspace(ctx, jobID, overlay, testbaseline.Durable(t, sqlDB, jobID, primary))
	testutil.FailErr(t, "SetWorkerWorkspace", err)
	testutil.FailErr(t, "SetMergeStatus", q.SetMergeStatus(ctx, jobID, api.WorkerMergeStatusPending))

	svc := &MergeService{Queue: q, Store: q, DataDir: t.TempDir()}
	previewOut, err := svc.PreviewForSession(ctx, "parent-cp-conf", jobID, "full", nil)
	testutil.FailErr(t, "PreviewForSession", err)
	if len(previewOut.Conflicts) != 2 {
		t.Fatalf("preview Conflicts = %+v, want 2 conflicts", previewOut.Conflicts)
	}
	var sawAmbiguous, sawThreeWay bool
	for _, c := range previewOut.Conflicts {
		joined := strings.Join(c.Summary, " ")
		if strings.Contains(joined, string(checkpoint.ConflictAmbiguousBase)) {
			sawAmbiguous = true
		}
		if strings.Contains(joined, string(checkpoint.ConflictContentOverlap)) {
			sawThreeWay = true
		}
	}
	if !sawAmbiguous || !sawThreeWay {
		t.Fatalf("expected ConflictAmbiguousBase and ConflictContentOverlap in summaries, got %+v", previewOut.Conflicts)
	}
}
