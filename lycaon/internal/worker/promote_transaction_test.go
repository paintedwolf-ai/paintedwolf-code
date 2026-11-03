package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/session"
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

	plans := []promoteMutation{{
		path: "script.sh", original: []byte("old\n"), originalExists: true, originalMode: 0o755,
		target: []byte("new\n"), targetExists: true,
	}}
	tx, err := beginPromoteTransaction(dataDir, task.ID, plans)
	testutil.FailErr(t, "begin transaction", err)
	promote := PromoteRootsForTask(task, nil)
	testutil.FailErr(t, "apply transaction", tx.apply(t.Context(), promote, task))

	found, err := recoverPromoteTransaction(dataDir, task.ID, promote, task)
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
	if !os.SameFile(info, aliasInfo) {
		t.Fatal("expected Stage-1 promoteTransaction recovery to restore hard-link identity")
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

	_, err = recoverPromoteTransaction(dataDir, task.ID, promote, task)
	if err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("recover error = %v, want divergence", err)
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

	err = tx.apply(t.Context(), PromoteRootsForTask(task, nil), task)
	var unstarted *promoteTransactionUnstartedError
	if !errors.As(err, &unstarted) {
		t.Fatalf("apply error = %v, want unstarted conflict", err)
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
	cpStoreDir := filepath.Join(overlay, session.TaskCheckpointStoreSubdir)
	cpStore, err := checkpoint.NewStore(cpStoreDir)
	testutil.FailErr(t, "NewStore", err)

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
