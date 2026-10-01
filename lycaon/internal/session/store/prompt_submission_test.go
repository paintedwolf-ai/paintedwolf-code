package store

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptSubmissionOriginHostInitiated(t *testing.T) {
	for _, test := range []struct {
		origin PromptSubmissionOrigin
		want   bool
	}{
		{origin: PromptSubmissionOriginUser},
		{origin: PromptSubmissionOriginLoopWake, want: true},
		{origin: PromptSubmissionOriginWorkerCloseout, want: true},
		{origin: PromptSubmissionOriginGroundingRetry, want: true},
		{origin: PromptSubmissionOrigin("unknown")},
	} {
		if got := test.origin.HostInitiated(); got != test.want {
			t.Errorf("HostInitiated(%q) = %v, want %v", test.origin, got, test.want)
		}
	}
}

func TestPromptSubmissionExactReplayAndFencedCompletion(t *testing.T) {
	ctx := context.Background()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := NewSQL(database)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	id := uuid.NewString()
	input := PromptSubmission{ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: "digest-a", InputJSON: `{"text":"ship"}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID}
	first, created, err := sessions.PutPromptSubmission(ctx, input)
	testutil.FailErr(t, "put first submission", err)
	if !created || first.Status != PromptSubmissionQueued {
		t.Fatalf("first = %+v created=%v", first, created)
	}
	replayed, created, err := sessions.PutPromptSubmission(ctx, input)
	testutil.FailErr(t, "put exact replay", err)
	if created || replayed.ID != first.ID {
		t.Fatalf("replay = %+v created=%v", replayed, created)
	}
	conflict := input
	conflict.InputDigest = "digest-b"
	if _, _, err := sessions.PutPromptSubmission(ctx, conflict); err == nil {
		t.Fatal("expected mutation id conflict")
	} else {
		var target *PromptSubmissionConflictError
		if !errors.As(err, &target) {
			t.Fatalf("conflict error = %v", err)
		}
	}
	claimed, won, err := sessions.ClaimPromptSubmission(ctx, id)
	testutil.FailErr(t, "claim submission", err)
	if !won || claimed.ClaimToken == "" {
		t.Fatalf("claim = %+v won=%v", claimed, won)
	}
	_, won, err = sessions.ClaimPromptSubmission(ctx, id)
	testutil.FailErr(t, "claim replay", err)
	if won {
		t.Fatal("second claimant won")
	}
	if err := sessions.FinishPromptSubmission(ctx, id, "wrong-token", PromptSubmissionComplete, `{}`, PromptSubmissionFailure{}); err == nil {
		t.Fatal("stale fencing token completed submission")
	}
	testutil.FailErr(t, "finish claimed submission", sessions.FinishPromptSubmission(ctx, id, claimed.ClaimToken, PromptSubmissionComplete, `{}`, PromptSubmissionFailure{}))
}

func TestPromptAttachmentRetentionTransfersToMessage(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := NewSQL(database)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	id := uuid.NewString()
	blobID := "0123456789abcdef"
	testutil.FailErr(t, "record attachment blob", sessions.RecordPromptAttachmentBlob(ctx, sess.ProjectID, blobID, 27))
	_, created, err := sessions.PutPromptSubmission(ctx, PromptSubmission{
		ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: "digest",
		InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID, AttachmentBlobIDs: []string{blobID},
	})
	testutil.FailErr(t, "admit prompt attachment", err)
	if !created {
		t.Fatal("prompt attachment admission replayed unexpectedly")
	}
	assertPromptAttachmentRetention(t, sessions, id, blobID)

	testutil.FailErr(t, "append retaining message", sessions.AppendMessages(ctx, sess.ID, api.Message{
		ID: id, Role: api.MessageRoleUser, Content: "attachment", CreatedAt: time.Now().UTC(),
		ContentParts: []api.MessageContentPart{{BlobID: blobID}},
	}))
	assertPromptAttachmentRetention(t, sessions, id, blobID)

	testutil.FailErr(t, "delete retaining session", sessions.Delete(ctx, sess.ID))
	retentions, err := sessions.ListOperationPromptAttachmentRetentions(ctx, id)
	testutil.FailErr(t, "list retentions after session delete", err)
	if len(retentions) != 0 {
		t.Fatalf("attachment retentions after session delete = %+v", retentions)
	}
}

func TestPromptAttachmentBlobUsageAndReclaimQueue(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := NewSQL(database)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	blobID := "blob-one"
	testutil.FailErr(t, "record attachment blob", sessions.RecordPromptAttachmentBlob(ctx, sess.ProjectID, blobID, 27))
	used, err := sessions.PromptAttachmentStorageUsage(ctx, sess.ProjectID)
	testutil.FailErr(t, "read attachment usage", err)
	if used != 27 {
		t.Fatalf("attachment usage = %d want 27", used)
	}
	old := time.Now().Add(-48 * time.Hour).UTC()
	_, err = database.ExecContext(ctx, `UPDATE prompt_attachment_blobs SET created_at = ? WHERE project_id = ? AND blob_id = ?`,
		old.Format(time.RFC3339Nano), sess.ProjectID, blobID)
	testutil.FailErr(t, "age attachment blob", err)
	candidates, err := sessions.ListPromptAttachmentReclaimCandidates(ctx, sess.ProjectID, time.Now(), 128)
	testutil.FailErr(t, "list unclaimed attachment", err)
	if len(candidates) != 1 || candidates[0] != blobID {
		t.Fatalf("unclaimed candidates = %v", candidates)
	}

	submissionID := uuid.NewString()
	_, _, err = sessions.PutPromptSubmission(ctx, PromptSubmission{
		ID: submissionID, SessionID: sess.ID, ProjectID: sess.ProjectID,
		InputDigest: "digest", InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
		AttachmentBlobIDs: []string{blobID},
	})
	testutil.FailErr(t, "retain attachment blob", err)
	candidates, err = sessions.ListPromptAttachmentReclaimCandidates(ctx, sess.ProjectID, time.Now(), 128)
	testutil.FailErr(t, "list retained attachment", err)
	if len(candidates) != 0 {
		t.Fatalf("retained candidates = %v", candidates)
	}
	if err := sessions.DeletePromptAttachmentBlob(ctx, sess.ProjectID, blobID); !errors.Is(err, ErrPromptAttachmentRetained) {
		t.Fatalf("delete retained attachment = %v", err)
	}

	testutil.FailErr(t, "delete retaining session", sessions.Delete(ctx, sess.ID))
	candidates, err = sessions.ListPromptAttachmentReclaimCandidates(ctx, sess.ProjectID, time.Now(), 128)
	testutil.FailErr(t, "list released attachment", err)
	if len(candidates) != 1 || candidates[0] != blobID {
		t.Fatalf("released candidates = %v", candidates)
	}
	testutil.FailErr(t, "delete attachment blob", sessions.DeletePromptAttachmentBlob(ctx, sess.ProjectID, blobID))
	used, err = sessions.PromptAttachmentStorageUsage(ctx, sess.ProjectID)
	testutil.FailErr(t, "read released attachment usage", err)
	if used != 0 {
		t.Fatalf("released attachment usage = %d want 0", used)
	}
}

func TestPromptAttachmentAdmissionReleasesOnTerminalReceipt(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := NewSQL(database)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	id := uuid.NewString()
	testutil.FailErr(t, "record attachment blob", sessions.RecordPromptAttachmentBlob(ctx, sess.ProjectID, "0123456789abcdef", 27))
	_, _, err = sessions.PutPromptSubmission(ctx, PromptSubmission{
		ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: "digest",
		InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID, AttachmentBlobIDs: []string{"0123456789abcdef"},
	})
	testutil.FailErr(t, "admit prompt attachment", err)
	testutil.FailErr(t, "cancel queued prompt", sessions.CancelQueuedPromptSubmissions(ctx, []string{id}))
	retentions, err := sessions.ListOperationPromptAttachmentRetentions(ctx, id)
	testutil.FailErr(t, "list retentions after cancellation", err)
	if len(retentions) != 0 {
		t.Fatalf("attachment retentions after cancellation = %+v", retentions)
	}
}

func TestMemoryPromptAttachmentRetentionsFollowReceiptAndMessageLifecycle(t *testing.T) {
	ctx := t.Context()
	sessions := NewMemory()
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	id := uuid.NewString()
	blobID := "0123456789abcdef"
	testutil.FailErr(t, "record attachment blob", sessions.RecordPromptAttachmentBlob(ctx, sess.ProjectID, blobID, 27))
	candidates, err := sessions.ListPromptAttachmentReclaimCandidates(ctx, sess.ProjectID, time.Now().Add(time.Minute), 1)
	testutil.FailErr(t, "list unclaimed attachment", err)
	if len(candidates) != 1 || candidates[0] != blobID {
		t.Fatalf("unclaimed candidates = %v", candidates)
	}
	_, _, err = sessions.PutPromptSubmission(ctx, PromptSubmission{
		ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: "digest",
		InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID, AttachmentBlobIDs: []string{blobID},
	})
	testutil.FailErr(t, "admit prompt attachment", err)
	retentions, err := sessions.ListOperationPromptAttachmentRetentions(ctx, id)
	testutil.FailErr(t, "list admission retention", err)
	if len(retentions) != 1 || retentions[0].BlobID != blobID {
		t.Fatalf("admission retentions = %+v", retentions)
	}
	candidates, err = sessions.ListPromptAttachmentReclaimCandidates(ctx, sess.ProjectID, time.Now().Add(time.Minute), 1)
	testutil.FailErr(t, "list retained attachment", err)
	if len(candidates) != 0 {
		t.Fatalf("retained candidates = %v", candidates)
	}
	claimed, won, err := sessions.ClaimPromptSubmission(ctx, id)
	testutil.FailErr(t, "claim prompt", err)
	if !won {
		t.Fatal("prompt claim lost")
	}
	testutil.FailErr(t, "append retaining message", sessions.AppendMessages(ctx, sess.ID, api.Message{
		ID: id, Role: api.MessageRoleUser, Content: "attachment", CreatedAt: time.Now().UTC(),
		ContentParts: []api.MessageContentPart{{BlobID: blobID}},
	}))
	testutil.FailErr(t, "finish prompt", sessions.FinishPromptSubmission(
		ctx, id, claimed.ClaimToken, PromptSubmissionComplete, `{}`, PromptSubmissionFailure{},
	))
	retentions, err = sessions.ListOperationPromptAttachmentRetentions(ctx, id)
	testutil.FailErr(t, "list message retention", err)
	if len(retentions) != 1 || retentions[0].OperationID != id || retentions[0].BlobID != blobID {
		t.Fatalf("message retentions = %+v", retentions)
	}
	testutil.FailErr(t, "delete session", sessions.Delete(ctx, sess.ID))
	retentions, err = sessions.ListOperationPromptAttachmentRetentions(ctx, id)
	testutil.FailErr(t, "list deleted retentions", err)
	if len(retentions) != 0 {
		t.Fatalf("retentions after session delete = %+v", retentions)
	}
}

func assertPromptAttachmentRetention(t *testing.T, sessions *SQL, operationID, blobID string) {
	t.Helper()
	retentions, err := sessions.ListOperationPromptAttachmentRetentions(t.Context(), operationID)
	testutil.FailErr(t, "list prompt attachment retentions", err)
	if len(retentions) != 1 || retentions[0].OperationID != operationID || retentions[0].BlobID != blobID {
		t.Fatalf("prompt attachment retentions = %+v", retentions)
	}
}

func TestPromptSubmissionRecoveryRequeuesStartedUserWork(t *testing.T) {
	ctx := context.Background()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := NewSQL(database)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	queuedID, runningID := uuid.NewString(), uuid.NewString()
	for _, id := range []string{queuedID, runningID} {
		_, _, err := sessions.PutPromptSubmission(ctx, PromptSubmission{
			ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: id, InputJSON: `{}`,
			Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
		})
		testutil.FailErr(t, "put submission", err)
	}
	_, won, err := sessions.ClaimPromptSubmission(ctx, runningID)
	testutil.FailErr(t, "claim running submission", err)
	if !won {
		t.Fatal("running submission was not claimed")
	}
	queued, err := sessions.RecoverPromptSubmissions(ctx)
	testutil.FailErr(t, "recover submissions", err)
	if len(queued) != 2 || queued[0] != queuedID || queued[1] != runningID {
		t.Fatalf("queued = %v", queued)
	}
	running, err := sessions.GetPromptSubmission(ctx, runningID)
	testutil.FailErr(t, "get recovered submission", err)
	if running.Status != PromptSubmissionQueued {
		t.Fatalf("status = %s", running.Status)
	}
}

func TestInterruptPromptSubmissionsBySessionClosesQueuedAndRunningOnlyForTarget(t *testing.T) {
	ctx := context.Background()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := NewSQL(database)
	target, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create target session", err)
	other, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create other session", err)

	queuedID, runningID, otherID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, input := range []PromptSubmission{
		{ID: queuedID, SessionID: target.ID, ProjectID: target.ProjectID, InputDigest: queuedID, InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: target.OwnerPersonID},
		{ID: runningID, SessionID: target.ID, ProjectID: target.ProjectID, InputDigest: runningID, InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: target.OwnerPersonID},
		{ID: otherID, SessionID: other.ID, ProjectID: other.ProjectID, InputDigest: otherID, InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: target.OwnerPersonID},
	} {
		_, _, err := sessions.PutPromptSubmission(ctx, input)
		testutil.FailErr(t, "put submission", err)
	}
	_, won, err := sessions.ClaimPromptSubmission(ctx, runningID)
	testutil.FailErr(t, "claim running submission", err)
	if !won {
		t.Fatal("running submission was not claimed")
	}

	testutil.FailErr(t, "interrupt target session", sessions.InterruptPromptSubmissionsBySession(ctx, target.ID))
	queued, err := sessions.GetPromptSubmission(ctx, queuedID)
	testutil.FailErr(t, "get canceled queued submission", err)
	running, err := sessions.GetPromptSubmission(ctx, runningID)
	testutil.FailErr(t, "get interrupted running submission", err)
	unrelated, err := sessions.GetPromptSubmission(ctx, otherID)
	testutil.FailErr(t, "get unrelated submission", err)
	if queued.Status != PromptSubmissionCanceled || running.Status != PromptSubmissionInterrupted {
		t.Fatalf("target statuses = %q/%q, want canceled/interrupted", queued.Status, running.Status)
	}
	if unrelated.Status != PromptSubmissionQueued {
		t.Fatalf("unrelated status = %q, want queued", unrelated.Status)
	}
}

func TestInterruptRunningPromptSubmissionsBySessionPreservesQueued(t *testing.T) {
	ctx := context.Background()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := NewSQL(database)
	target, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create target session", err)

	queuedID, runningID := uuid.NewString(), uuid.NewString()
	for _, input := range []PromptSubmission{
		{ID: queuedID, SessionID: target.ID, ProjectID: target.ProjectID, InputDigest: queuedID, InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: target.OwnerPersonID},
		{ID: runningID, SessionID: target.ID, ProjectID: target.ProjectID, InputDigest: runningID, InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: target.OwnerPersonID},
	} {
		_, _, err := sessions.PutPromptSubmission(ctx, input)
		testutil.FailErr(t, "put submission", err)
	}
	_, won, err := sessions.ClaimPromptSubmission(ctx, runningID)
	testutil.FailErr(t, "claim running submission", err)
	if !won {
		t.Fatal("running submission was not claimed")
	}

	testutil.FailErr(t, "interrupt running target submission", sessions.InterruptRunningPromptSubmissionsBySession(ctx, target.ID))
	queued, err := sessions.GetPromptSubmission(ctx, queuedID)
	testutil.FailErr(t, "get preserved queued submission", err)
	running, err := sessions.GetPromptSubmission(ctx, runningID)
	testutil.FailErr(t, "get interrupted running submission", err)
	if queued.Status != PromptSubmissionQueued || running.Status != PromptSubmissionInterrupted {
		t.Fatalf("target statuses = %q/%q, want queued/interrupted", queued.Status, running.Status)
	}
}

func TestPromptSubmissionCancelOnlyQueued(t *testing.T) {
	ctx := context.Background()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := NewSQL(database)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	queuedID := uuid.NewString()
	_, _, err = sessions.PutPromptSubmission(ctx, PromptSubmission{ID: queuedID, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: "d1", InputJSON: `{"text":"a"}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID})
	testutil.FailErr(t, "put queued submission", err)
	runningID := uuid.NewString()
	_, _, err = sessions.PutPromptSubmission(ctx, PromptSubmission{ID: runningID, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: "d2", InputJSON: `{"text":"b"}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID})
	testutil.FailErr(t, "put running submission", err)
	_, won, err := sessions.ClaimPromptSubmission(ctx, runningID)
	testutil.FailErr(t, "claim running submission", err)
	if !won {
		t.Fatal("claim lost")
	}

	testutil.FailErr(t, "cancel queued", sessions.CancelQueuedPromptSubmissions(ctx, []string{queuedID}))
	row, err := sessions.GetPromptSubmission(ctx, queuedID)
	testutil.FailErr(t, "get canceled row", err)
	if row.Status != PromptSubmissionCanceled || row.CompletedAt == nil {
		t.Fatalf("canceled row = %+v", row)
	}

	testutil.FailErr(t, "cancel running", sessions.CancelQueuedPromptSubmissions(ctx, []string{runningID}))

	// Batch cancellation leaves claimed rows untouched.
	otherID := uuid.NewString()
	_, _, err = sessions.PutPromptSubmission(ctx, PromptSubmission{ID: otherID, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: "d3", InputJSON: `{"text":"c"}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID})
	testutil.FailErr(t, "put third submission", err)
	testutil.FailErr(t, "cancel batch", sessions.CancelQueuedPromptSubmissions(ctx, []string{otherID}))
	row, err = sessions.GetPromptSubmission(ctx, otherID)
	testutil.FailErr(t, "get third row", err)
	if row.Status != PromptSubmissionCanceled {
		t.Fatalf("third row = %+v, want canceled", row)
	}
	row, err = sessions.GetPromptSubmission(ctx, runningID)
	testutil.FailErr(t, "get running row", err)
	if row.Status != PromptSubmissionRunning {
		t.Fatalf("running row = %+v, want still running", row)
	}

	// Recovery excludes canceled rows and requeues the claimed user receipt.
	ids, err := sessions.RecoverPromptSubmissions(ctx)
	testutil.FailErr(t, "recover", err)
	if len(ids) != 1 || ids[0] != runningID {
		t.Fatalf("recovered ids = %v, want running receipt", ids)
	}
}

func TestCancelQueuedPromptSubmissionsIsOneAtomicBatch(t *testing.T) {
	ctx := context.Background()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := NewSQL(database)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	ids := []string{uuid.NewString(), uuid.NewString()}
	for _, id := range ids {
		_, _, err := sessions.PutPromptSubmission(ctx, PromptSubmission{
			ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: id,
			InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
		})
		testutil.FailErr(t, "put submission", err)
	}
	testutil.FailErr(t, "cancel batch", sessions.CancelQueuedPromptSubmissions(ctx, ids))
	for _, id := range ids {
		row, err := sessions.GetPromptSubmission(ctx, id)
		testutil.FailErr(t, "get canceled submission", err)
		if row.Status != PromptSubmissionCanceled {
			t.Fatalf("submission %s status = %s, want canceled", id, row.Status)
		}
	}
}

func TestUpdateQueuedPromptSubmissionInputsIsOneAtomicBatch(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := NewSQL(database)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	ids := []string{uuid.NewString(), uuid.NewString()}
	for _, id := range ids {
		_, _, putErr := sessions.PutPromptSubmission(ctx, PromptSubmission{
			ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: id,
			InputJSON: `{"text":"before"}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
		})
		testutil.FailErr(t, "put submission", putErr)
	}
	_, claimed, err := sessions.ClaimPromptSubmission(ctx, ids[1])
	testutil.FailErr(t, "claim second submission", err)
	if !claimed {
		t.Fatal("second submission was not claimed")
	}
	updated, err := sessions.UpdateQueuedPromptSubmissionInputs(ctx, []PromptSubmissionInputUpdate{
		{ID: ids[0], InputJSON: `{"text":"after"}`},
		{ID: ids[1], InputJSON: `{"text":"after"}`},
	})
	testutil.FailErr(t, "update receipt batch", err)
	if updated {
		t.Fatal("partially claimable receipt batch reported success")
	}
	first, err := sessions.GetPromptSubmission(ctx, ids[0])
	testutil.FailErr(t, "read first submission", err)
	if first.InputJSON != `{"text":"before"}` {
		t.Fatalf("partial batch escaped rollback: %s", first.InputJSON)
	}
}

func TestClaimPromptSubmissionsRollsBackPartialBatch(t *testing.T) {
	ctx := context.Background()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := NewSQL(database)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	ids := []string{uuid.NewString(), uuid.NewString()}
	for _, id := range ids {
		_, _, putErr := sessions.PutPromptSubmission(ctx, PromptSubmission{
			ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: id,
			InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
		})
		testutil.FailErr(t, "put submission", putErr)
	}
	_, won, err := sessions.ClaimPromptSubmission(ctx, ids[1])
	testutil.FailErr(t, "preclaim second submission", err)
	if !won {
		t.Fatal("preclaim second submission lost")
	}

	claimed, allClaimed, err := sessions.ClaimPromptSubmissions(ctx, ids)
	testutil.FailErr(t, "claim batch", err)
	if allClaimed || len(claimed) != 0 {
		t.Fatalf("claim batch = (%+v, %v), want no claims", claimed, allClaimed)
	}
	first, err := sessions.GetPromptSubmission(ctx, ids[0])
	testutil.FailErr(t, "get first submission", err)
	if first.Status != PromptSubmissionQueued || first.ClaimToken != "" {
		t.Fatalf("partial batch escaped rollback: %+v", first)
	}
}

func TestListQueuedUserPromptSubmissionsUsesSequenceNotWallClock(t *testing.T) {
	ctx := context.Background()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := NewSQL(database)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	firstAcceptedID, secondAcceptedID := uuid.NewString(), uuid.NewString()
	for _, input := range []PromptSubmission{
		{ID: firstAcceptedID, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: firstAcceptedID, InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID, CreatedAt: base.Add(time.Second)},
		{ID: secondAcceptedID, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: secondAcceptedID, InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID, CreatedAt: base},
	} {
		_, _, putErr := sessions.PutPromptSubmission(ctx, input)
		testutil.FailErr(t, "put submission", putErr)
	}

	queued, err := sessions.ListQueuedUserPromptSubmissions(ctx, sess.ID)
	testutil.FailErr(t, "list queued submissions", err)
	if len(queued) != 2 || queued[0].ID != firstAcceptedID || queued[1].ID != secondAcceptedID {
		t.Fatalf("queued order = %+v, want insertion sequence despite reversed timestamps", queued)
	}
}
