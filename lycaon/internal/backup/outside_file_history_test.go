package backup_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOutsideFileHistorySurvivesBackupAfterItsFirstChatIsDeleted(t *testing.T) {
	ctx := t.Context()
	config := t.TempDir()
	dbPath := filepath.Join(config, "store.db")
	database := testdbfixture.OpenPath(t, dbPath)
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertProjectRoot(t, database, projectID, t.TempDir())
	chats := store.NewSQL(database)
	first, err := chats.Create(ctx, api.CreateSessionRequest{}, projectID)
	testutil.FailErr(t, "create first chat", err)
	second, err := chats.Create(ctx, api.CreateSessionRequest{}, projectID)
	testutil.FailErr(t, "create second chat", err)

	ledger := sourceledger.New(database, filepath.Join(config, "source-content"))
	outside, err := db.New(database).GetProjectOutsideRootID(ctx, projectID)
	testutil.FailErr(t, "resolve outside namespace", err)
	input := sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: outside, Path: "work/notes.txt"},
		ProjectID:      projectID, SessionID: first.ID,
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("first\n"),
	}
	testutil.FailErr(t, "record first outside change", ledger.Record(ctx, input))
	input.SessionID, input.Op, input.Before, input.After = second.ID, api.SourceChangeOpWrite, input.After, []byte("second\n")
	testutil.FailErr(t, "record second outside change", ledger.Record(ctx, input))
	var effectID string
	testutil.FailErr(t, "read retained effect", database.QueryRowContext(ctx, `SELECT e.id FROM source_effects e
		JOIN source_operations o ON o.id = e.operation_id WHERE o.session_id = ?`, second.ID).Scan(&effectID))
	testutil.FailErr(t, "delete first chat", chats.Delete(ctx, first.ID))

	archive, _, err := createArchive(t, ctx, backup.CreateOpts{ConfigDir: config, DBPath: dbPath, SQLDB: database, SchemaUserVersion: db.SchemaVersion})
	testutil.FailErr(t, "back up outside history", err)
	target := t.TempDir()
	targetPath := filepath.Join(target, "store.db")
	targetDB := testdbfixture.OpenPath(t, targetPath)
	_, err = backup.Stage(ctx, backup.StageOpts{ConfigDir: target, DBPath: targetPath, SQLDB: targetDB, ArchivePath: archivePath(t, archive), SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage backup", err)
	testutil.FailErr(t, "close replaced store", targetDB.Close())
	testutil.FailErr(t, "restore outside history", backup.ApplyPending(ctx, target))

	restored := testdbfixture.OpenPath(t, targetPath)
	restoredLedger := sourceledger.New(restored, filepath.Join(target, "source-content"))
	diff, err := restoredLedger.Comparisons.CompareEffect(ctx, projectID, effectID)
	testutil.FailErr(t, "compare restored effect", err)
	if diff.Before.Content != "first\n" || diff.After.Content != "second\n" {
		t.Fatalf("restored comparison = %q to %q", diff.Before.Content, diff.After.Content)
	}
	refs, err := db.New(restored).ListSourceChatFiles(ctx, second.ID)
	testutil.FailErr(t, "load restored reference", err)
	if len(refs) != 1 || refs[0].FileID != diff.FileID || refs[0].Changed != 1 {
		t.Fatalf("restored references = %+v, want the changed file %s", refs, diff.FileID)
	}
}
