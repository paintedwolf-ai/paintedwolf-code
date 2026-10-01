package store

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSQLStoreProgressUpdateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "store.db")

	sqlDB := testdbfixture.OpenPath(t, dbPath)
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	const runID = "run-progress-roundtrip"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO workflow_runs (id, session_id, project_id, workflow_id, workflow_version, status, current_phase, created_at, updated_at)
		VALUES (?, ?, ?, 'plan', '1.0.0', 'running', 'intake', ?, ?)
	`, runID, sess.ID, testdbseed.DefaultProjectID, now, now)
	testutil.FailErr(t, "seed workflow run", err)

	meta := &api.ProgressUpdateMeta{
		Seq: 2,
		Changes: []api.ProgressChange{
			{Kind: api.ProgressChangeDone, Label: "Engine module", State: "done"},
			{Kind: api.ProgressChangeDone, Label: "AI module", State: "done"},
		},
	}
	testutil.FailErr(t, "append progress_update", store.AppendMessages(ctx, sess.ID, api.Message{
		ID:             "pu-1",
		Role:           api.MessageRoleSystem,
		Kind:           api.MessageKindProgressUpdate,
		WorkflowRunID:  runID,
		ProgressUpdate: meta,
		Visibility:     api.MessageVisibilityTranscript,
	}))
	testutil.FailErr(t, "close database for reopen", sqlDB.Close())

	sqlDB2 := testdbfixture.OpenPath(t, dbPath)

	msgs, err := NewSQL(sqlDB2).GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	got := msgs[0].ProgressUpdate
	if got == nil {
		t.Fatal("ProgressUpdate nil after reload")
	}
	if got.Seq != meta.Seq || len(got.Changes) != len(meta.Changes) {
		t.Fatalf("ProgressUpdate = %+v, want %+v", got, meta)
	}
	if msgs[0].WorkflowRunID != runID {
		t.Fatalf("workflow_run_id = %q want %q", msgs[0].WorkflowRunID, runID)
	}
}
