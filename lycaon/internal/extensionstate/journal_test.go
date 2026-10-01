package extensionstate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func journalDB(t *testing.T) db.Handle {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "journal.db")
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO projects(id, name, last_opened_at, created_at) VALUES('proj-1','P','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed project", err)
	return sqlDB
}

type fakeInvalidator struct {
	invalidated []string
}

func (f *fakeInvalidator) InvalidateProjects(_ context.Context, projectID string) {
	f.invalidated = append(f.invalidated, projectID)
}

type fakeEmitter struct {
	scopes []Scope
}

func (f *fakeEmitter) ExtensionsChanged(_ context.Context, scope Scope) {
	f.scopes = append(f.scopes, scope)
}

func journalOp(t *testing.T, dir string) Operation {
	t.Helper()
	return Operation{
		Scope:           "project",
		ProjectID:       "proj-1",
		ProjectDir:      dir,
		DesiredPath:     filepath.Join(dir, "extensions.yaml"),
		LockPath:        filepath.Join(dir, "extensions.lock.yaml"),
		PrevDesiredGone: true,
		PrevLockGone:    true,
		NextDesired:     []byte("format: 1\npacks: []\ndisabled: []\nown: {}\n"),
		NextLock:        []byte("lock_format: 1\npackages: []\n"),
	}
}

func journalPresent(t *testing.T, sqlDB db.Handle, id string) bool {
	t.Helper()
	var count int
	testutil.FailErr(t, "count journal row",
		sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM extension_operations WHERE id=?`, id).Scan(&count))
	return count > 0
}

func TestRecoverRestoresInterruptedDevicePublication(t *testing.T) {
	sqlDB := journalDB(t)
	journal := NewSQLJournal(sqlDB)
	dir := t.TempDir()
	op := journalOp(t, dir)
	op.Scope = "device"
	op.ProjectID = ""
	op.ProjectDir = ""

	id, err := journal.Begin(t.Context(), op)
	testutil.FailErr(t, "begin", err)
	testutil.FailErr(t, "write lock", os.WriteFile(op.LockPath, op.NextLock, 0o600))

	invalidator := &fakeInvalidator{}
	emitter := &fakeEmitter{}
	testutil.FailErr(t, "recover", journal.Recover(t.Context(), invalidator, emitter))

	if journalPresent(t, sqlDB, id) {
		t.Fatal("settled device operation remained in the journal")
	}
	if _, err := os.Stat(op.LockPath); !os.IsNotExist(err) {
		t.Fatal("interrupted device lock file must be restored to its previous absence")
	}
	if len(invalidator.invalidated) != 0 || len(emitter.scopes) != 0 {
		t.Fatal("an abandoned device operation must not publish")
	}
}

func TestRecoverRestoresInterruptedPublication(t *testing.T) {
	sqlDB := journalDB(t)
	journal := NewSQLJournal(sqlDB)
	dir := t.TempDir()
	op := journalOp(t, dir)

	id, err := journal.Begin(t.Context(), op)
	testutil.FailErr(t, "begin", err)
	testutil.FailErr(t, "write lock", os.WriteFile(op.LockPath, op.NextLock, 0o600))

	testutil.FailErr(t, "recover", journal.Recover(t.Context(), nil, nil))

	if journalPresent(t, sqlDB, id) {
		t.Fatal("settled operation remained in the journal")
	}
	if _, err := os.Stat(op.LockPath); !os.IsNotExist(err) {
		t.Fatal("interrupted lock file must be restored to its previous absence")
	}
}

func TestRecoverCompletesPublishedTransition(t *testing.T) {
	sqlDB := journalDB(t)
	journal := NewSQLJournal(sqlDB)
	dir := t.TempDir()
	op := journalOp(t, dir)

	id, err := journal.Begin(t.Context(), op)
	testutil.FailErr(t, "begin", err)
	testutil.FailErr(t, "write desired", os.WriteFile(op.DesiredPath, op.NextDesired, 0o600))
	testutil.FailErr(t, "write lock", os.WriteFile(op.LockPath, op.NextLock, 0o600))

	invalidator := &fakeInvalidator{}
	emitter := &fakeEmitter{}
	testutil.FailErr(t, "recover", journal.Recover(t.Context(), invalidator, emitter))

	if journalPresent(t, sqlDB, id) {
		t.Fatal("completed operation remained in the journal")
	}
	if len(invalidator.invalidated) != 1 || invalidator.invalidated[0] != "proj-1" {
		t.Fatalf("invalidated = %v", invalidator.invalidated)
	}
	if len(emitter.scopes) != 1 || emitter.scopes[0].ProjectID != "proj-1" {
		t.Fatalf("emitted = %+v", emitter.scopes)
	}
	if data, err := os.ReadFile(op.DesiredPath); err != nil || string(data) != string(op.NextDesired) {
		t.Fatalf("published desired must survive recovery: %v", err)
	}
}

func TestRecoverFilesAppliedWithPreviousBytes(t *testing.T) {
	sqlDB := journalDB(t)
	journal := NewSQLJournal(sqlDB)
	dir := t.TempDir()
	op := journalOp(t, dir)
	op.PrevDesiredGone = false
	op.PrevDesired = []byte("format: 1\npacks: []\ndisabled: [guidance/old]\nown: {}\n")

	id, err := journal.Begin(t.Context(), op)
	testutil.FailErr(t, "begin", err)
	testutil.FailErr(t, "write desired", os.WriteFile(op.DesiredPath, op.NextDesired, 0o600))
	testutil.FailErr(t, "write lock", os.WriteFile(op.LockPath, op.NextLock, 0o600))
	testutil.FailErr(t, "mark files applied", journal.FilesApplied(t.Context(), id))

	testutil.FailErr(t, "recover", journal.Recover(t.Context(), nil, nil))
	if journalPresent(t, sqlDB, id) {
		t.Fatal("completed files_applied operation remained in the journal")
	}
}

func TestFinishRemovesOperation(t *testing.T) {
	sqlDB := journalDB(t)
	journal := NewSQLJournal(sqlDB)
	op := journalOp(t, t.TempDir())

	id, err := journal.Begin(t.Context(), op)
	testutil.FailErr(t, "begin", err)
	testutil.FailErr(t, "finish", journal.Finish(t.Context(), id))
	if journalPresent(t, sqlDB, id) {
		t.Fatal("finished operation remained in the journal")
	}
}

// The project directory identifies recovery; the registry id is optional metadata.
func TestJournalRejectsMismatchedScopeIdentity(t *testing.T) {
	sqlDB := journalDB(t)
	journal := NewSQLJournal(sqlDB)

	tests := []struct {
		name       string
		scope      string
		projectDir string
	}{
		{name: "project without dir", scope: "project"},
		{name: "device with dir", scope: "device", projectDir: t.TempDir()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := journalOp(t, t.TempDir())
			op.Scope = tt.scope
			op.ProjectID = ""
			op.ProjectDir = tt.projectDir
			if _, err := journal.Begin(t.Context(), op); err == nil {
				t.Fatal("mismatched scope identity was stored")
			}
		})
	}
}

// CLI mutations recover without a registry id.
func TestJournalAcceptsProjectOperationWithoutRegistryID(t *testing.T) {
	sqlDB := journalDB(t)
	journal := NewSQLJournal(sqlDB)
	op := journalOp(t, t.TempDir())
	op.ProjectID = ""
	if _, err := journal.Begin(t.Context(), op); err != nil {
		t.Fatalf("project operation keyed by directory was refused: %v", err)
	}
}
