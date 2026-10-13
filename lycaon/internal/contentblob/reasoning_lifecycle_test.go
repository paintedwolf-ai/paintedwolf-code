package contentblob_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func settleReasoning(t *testing.T, history *store.SQL, session *api.Session) string {
	t.Helper()
	turn, err := history.BeginTurn(t.Context(), store.TurnStart{SessionID: session.ID, ProjectID: session.ProjectID, Origin: store.TurnOriginUser, InputJSON: "{}"})
	testutil.FailErr(t, "begin reasoning turn", err)
	reasoning := &api.ModelReasoning{ProviderID: "provider", Model: "model", Text: "Check the source before editing.", Details: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"Check the source before editing.","signature":"signed-block"}`)}}
	raw, err := json.Marshal(reasoning)
	testutil.FailErr(t, "encode reasoning", err)
	output, err := history.SettleModelOutput(t.Context(), store.ModelOutput{ID: uuid.NewString(), TurnAttemptID: turn.Attempt.ID, SessionID: session.ID, Iteration: 1, MessageID: uuid.NewString(), ProviderID: reasoning.ProviderID, Model: reasoning.Model, Content: "Done.", ReasoningJSON: string(raw)})
	testutil.FailErr(t, "settle reasoning", err)
	testutil.FailErr(t, "project reasoning", history.AppendMessages(t.Context(), session.ID, api.Message{ID: output.MessageID, Role: api.MessageRoleAssistant, Content: output.Content, ModelReasoning: reasoning}))
	testutil.FailErr(t, "acknowledge reasoning projection", history.MarkModelOutputProjected(t.Context(), output.ID))
	_, err = history.FinishTurn(t.Context(), turn.Turn.ID, turn.Attempt.ID, store.TurnStatusComplete, output.ID, "{}", "")
	testutil.FailErr(t, "finish reasoning turn", err)
	return output.ID
}

func TestReasoningRetentionAndChatTreeDeletionUseProjectBlobLifecycle(t *testing.T) {
	database, dataDir := openGCTestDB(t)
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	history := store.NewSQL(database)
	history.SetDataDir(dataDir)
	deps := gcDeps(t, database, dataDir)
	root, err := history.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root chat", err)
	child, err := history.CreateChild(t.Context(), root, api.SpawnChildRequest{AgentType: "implement"})
	testutil.FailErr(t, "create child chat", err)
	other, err := history.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create retained chat", err)
	rootOutput := settleReasoning(t, history, root)
	settleReasoning(t, history, child)
	settleReasoning(t, history, other)
	var sha string
	testutil.FailErr(t, "read canonical identity", database.QueryRowContext(t.Context(), `SELECT content_blob_sha256 FROM model_outputs WHERE id=?`, rootOutput).Scan(&sha))
	old := db.FormatTime(time.Now().AddDate(-2, 0, 0))
	_, err = database.ExecContext(t.Context(), `UPDATE model_outputs SET created_at=?,settled_at=?`, old, old)
	testutil.FailErr(t, "age retained outputs", err)
	_, err = db.RunRetention(t.Context(), database, db.DefaultRetention())
	testutil.FailErr(t, "maintain old history", err)
	reloaded := store.NewSQL(database)
	messages, err := reloaded.GetMessages(t.Context(), other.ID)
	testutil.FailErr(t, "reload old reasoning", err)
	if len(messages) != 1 || messages[0].ModelReasoning == nil || len(messages[0].ModelReasoning.Details) != 1 {
		t.Fatal("maintenance discarded retained reasoning")
	}
	testutil.FailErr(t, "delete root chat tree", history.Delete(t.Context(), root.ID))
	var outputs, projections int
	testutil.FailErr(t, "count surviving outputs", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM model_outputs`).Scan(&outputs))
	testutil.FailErr(t, "count surviving reasoning", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages WHERE reasoning_json IS NOT NULL`).Scan(&projections))
	if outputs != 1 || projections != 1 {
		t.Fatalf("chat tree deletion left outputs=%d projections=%d", outputs, projections)
	}
	_, err = contentblob.RunGCBatch(t.Context(), deps)
	testutil.FailErr(t, "maintain shared envelope", err)
	blobStore := contentblob.StoreFor(dataDir, root.ProjectID)
	_, err = contentblob.Read(blobStore, sha)
	testutil.FailErr(t, "preserve retained chat envelope", err)
	testutil.FailErr(t, "delete final chat", history.Delete(t.Context(), other.ID))
	processed, err := contentblob.RunGCBatch(t.Context(), deps)
	testutil.FailErr(t, "reclaim reasoning envelope", err)
	if processed != 1 {
		t.Fatalf("reclaimed %d envelopes, want 1 shared envelope", processed)
	}
	_, err = contentblob.Read(blobStore, sha)
	if !errors.Is(err, contentblob.ErrMissing) {
		t.Fatalf("deleted chat envelope remains readable: %v", err)
	}
}

func TestProjectDeletionReconciliationReclaimsRetainedReasoning(t *testing.T) {
	database, dataDir := openGCTestDB(t)
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	history := store.NewSQL(database)
	history.SetDataDir(dataDir)
	session, err := history.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create project chat", err)
	output := settleReasoning(t, history, session)
	var sha string
	testutil.FailErr(t, "read project envelope", database.QueryRowContext(t.Context(), `SELECT content_blob_sha256 FROM model_outputs WHERE id=?`, output).Scan(&sha))
	testutil.FailErr(t, "delete owning project", project.NewSQLRegistry(database).Delete(t.Context(), session.ProjectID))
	// Startup reconciliation finishes deletion interrupted after the database commit.
	removed, err := project.ReconcileHostStorage(dataDir, map[string]struct{}{})
	testutil.FailErr(t, "reconcile deleted project storage", err)
	if removed != 1 {
		t.Fatalf("removed %d project trees, want 1", removed)
	}
	_, err = contentblob.Read(contentblob.StoreFor(dataDir, session.ProjectID), sha)
	if !errors.Is(err, contentblob.ErrMissing) {
		t.Fatalf("deleted project retained reasoning: %v", err)
	}
}
