package backup_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectReasoningSurvivesRelocatedBackupRestore(t *testing.T) {
	source := t.TempDir()
	sourcePath := filepath.Join(source, "store.db")
	database := testdbfixture.OpenPath(t, sourcePath)
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	history := store.NewSQL(database)
	history.SetDataDir(source)
	session, err := history.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create retained chat", err)
	turn, err := history.BeginTurn(t.Context(), store.TurnStart{SessionID: session.ID, ProjectID: session.ProjectID, Origin: store.TurnOriginUser, InputJSON: "{}"})
	testutil.FailErr(t, "begin retained turn", err)
	want := &api.ModelReasoning{ProviderID: "provider", Model: "model", Text: "Inspect the declaration.", Details: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"Inspect the declaration.","signature":"original-signature"}`)}}
	reasoning, err := json.Marshal(want)
	testutil.FailErr(t, "encode signed reasoning", err)
	output, err := history.SettleModelOutput(t.Context(), store.ModelOutput{ID: uuid.NewString(), TurnAttemptID: turn.Attempt.ID, SessionID: session.ID, Iteration: 1, MessageID: uuid.NewString(), ProviderID: want.ProviderID, Model: want.Model, Content: "Complete.", ReasoningJSON: string(reasoning)})
	testutil.FailErr(t, "settle retained output", err)
	testutil.FailErr(t, "project retained output", history.AppendMessages(t.Context(), session.ID, api.Message{ID: output.MessageID, Role: api.MessageRoleAssistant, Content: output.Content, ModelReasoning: want}))
	testutil.FailErr(t, "acknowledge retained output", history.MarkModelOutputProjected(t.Context(), output.ID))
	_, err = history.FinishTurn(t.Context(), turn.Turn.ID, turn.Attempt.ID, store.TurnStatusComplete, output.ID, "{}", "")
	testutil.FailErr(t, "finish retained turn", err)
	archive, _, err := createArchive(t, t.Context(), backup.CreateOpts{ConfigDir: source, DBPath: sourcePath, SQLDB: database, SchemaUserVersion: db.SchemaVersion, AppVersion: "test"})
	testutil.FailErr(t, "capture project reasoning", err)
	target := t.TempDir()
	targetPath := filepath.Join(target, "store.db")
	targetDB := testdbfixture.OpenPath(t, targetPath)
	_, err = backup.Stage(t.Context(), backup.StageOpts{ConfigDir: target, DBPath: targetPath, SQLDB: targetDB, ArchivePath: archivePath(t, archive), SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage relocated reasoning", err)
	testutil.FailErr(t, "close replaced store", targetDB.Close())
	testutil.FailErr(t, "restore project reasoning", backup.ApplyPending(t.Context(), target))
	restored := testdbfixture.OpenPath(t, targetPath)
	restoredHistory := store.NewSQL(restored)
	restoredHistory.SetDataDir(target)
	messages, err := restoredHistory.GetMessages(t.Context(), session.ID)
	testutil.FailErr(t, "reload restored chat", err)
	if len(messages) != 1 || !reflect.DeepEqual(messages[0].ModelReasoning, want) {
		t.Fatal("restored chat lost exact reasoning or provenance")
	}
	var sha string
	testutil.FailErr(t, "read restored envelope identity", restored.QueryRowContext(t.Context(), `SELECT content_blob_sha256 FROM model_outputs WHERE id=?`, output.ID).Scan(&sha))
	raw, err := contentblob.Read(contentblob.StoreFor(target, session.ProjectID), sha)
	testutil.FailErr(t, "load restored canonical envelope", err)
	var envelope struct {
		Reasoning json.RawMessage `json:"reasoning_json"`
	}
	testutil.FailErr(t, "decode restored canonical reasoning", json.Unmarshal(raw, &envelope))
	if string(envelope.Reasoning) != string(reasoning) {
		t.Fatal("canonical reasoning changed during restore")
	}
	testutil.FailErr(t, "delete restored chat", restoredHistory.Delete(t.Context(), session.ID))
	var count int
	testutil.FailErr(t, "count restored reasoning after deletion", restored.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages WHERE reasoning_json IS NOT NULL`).Scan(&count))
	if count != 0 {
		t.Fatal("restored chat deletion retained reasoning")
	}
}
