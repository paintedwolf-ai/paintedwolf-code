package backup_test

import (
	"errors"
	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoredRewindJournalUsesDestination(t *testing.T) {
	source, project := t.TempDir(), t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(source, "store.db"))
	testdbseed.InsertSessionWithRoot(t, database, "review-session", testdbseed.DefaultProjectID, project)
	repository := store.NewSQL(database)
	checkpoint := sessioncheckpoint.New(source, project, repository)
	journal, err := sessioncheckpoint.NewJournal(checkpoint, &sessioncheckpoint.Manifest{SessionID: "review-session", AnchorMessageID: "anchor", ProjectDir: project}, "review-rewind", "review-session", &sourcerewind.Operation{})
	testutil.FailErr(t, "create journal", err)
	testutil.FailErr(t, "write journal", journal.Write())
	testutil.FailErr(t, "prepare receipt", repository.PrepareRewind(t.Context(), store.RewindOperation{ID: "review-rewind", SessionID: "review-session", AnchorMessageID: "anchor", InputDigest: "probe", ProjectDir: project}))
	archive, _, err := createArchive(t, t.Context(), backup.CreateOpts{ConfigDir: source, DBPath: filepath.Join(source, "store.db"), SQLDB: database, SchemaUserVersion: db.SchemaVersion, AppVersion: "test"})
	testutil.FailErr(t, "export pending rewind", err)
	target := t.TempDir()
	targetDB := testdbfixture.OpenPath(t, filepath.Join(target, "store.db"))
	_, err = backup.Stage(t.Context(), backup.StageOpts{ConfigDir: target, DBPath: filepath.Join(target, "store.db"), SQLDB: targetDB, ArchivePath: archivePath(t, archive), SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage relocated backup", err)
	testutil.FailErr(t, "close destination", targetDB.Close())
	testutil.FailErr(t, "restore", backup.ApplyPending(t.Context(), target))
	restored := testdbfixture.OpenPath(t, filepath.Join(target, "store.db"))
	row, err := store.NewSQL(restored).GetRewindOperation(t.Context(), "review-rewind")
	testutil.FailErr(t, "read restored receipt", err)
	relative, err := filepath.Rel(source, journal.Path())
	testutil.FailErr(t, "resolve journal", err)
	_, err = sessioncheckpoint.LoadJournal(filepath.Join(target, relative))
	testutil.FailErr(t, "read restored journal", err)
	testutil.FailErr(t, "make original unavailable", os.Remove(journal.Path()))
	relocated, err := sessioncheckpoint.LoadOperationJournal(target, *row)
	testutil.FailErr(t, "load destination journal", err)
	if relocated.Path() != filepath.Join(target, relative) {
		t.Fatalf("journal escaped destination: %s", relocated.Path())
	}
}

func TestBackupRefusesMissingOrMismatchedRewindJournal(t *testing.T) {
	config, project := t.TempDir(), t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(config, "store.db"))
	testdbseed.InsertSessionWithRoot(t, database, "session", testdbseed.DefaultProjectID, project)
	repository := store.NewSQL(database)
	checkpoint := sessioncheckpoint.New(config, project, repository)
	journal, err := sessioncheckpoint.NewJournal(checkpoint, &sessioncheckpoint.Manifest{SessionID: "session", AnchorMessageID: "anchor", ProjectDir: project}, "operation", "session", &sourcerewind.Operation{})
	testutil.FailErr(t, "create journal", err)
	testutil.FailErr(t, "prepare receipt", repository.PrepareRewind(t.Context(), store.RewindOperation{ID: "operation", SessionID: "session", AnchorMessageID: "anchor", InputDigest: "digest", ProjectDir: project}))
	opts := backup.CreateOpts{ConfigDir: config, DBPath: filepath.Join(config, "store.db"), SQLDB: database, SchemaUserVersion: db.SchemaVersion}
	destination := filepath.Join(t.TempDir(), "backup.zip")
	for _, state := range []string{"missing", "mismatched", "unsupported"} {
		t.Run(state, func(t *testing.T) {
			if state != "missing" {
				journal.TranscriptID = "other-session"
				if state == "unsupported" {
					journal.TranscriptID = "session"
					journal.Version = 999
				}
				testutil.FailErr(t, "write invalid journal", journal.Write())
			}
			if _, err := backup.Create(t.Context(), opts, destination); !errors.Is(err, backup.ErrInvalid) {
				t.Fatalf("invalid journal accepted: %v", err)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("invalid backup published: %v", err)
			}
		})
	}
	journal.Version = 2
	testutil.FailErr(t, "write valid journal", journal.Write())
	raw, manifest, err := createArchive(t, t.Context(), opts)
	testutil.FailErr(t, "create valid archive", err)
	rel, err := sessioncheckpoint.JournalRelPath(project, "operation")
	testutil.FailErr(t, "resolve journal", err)
	incomplete := omitRetainedFile(t, raw, manifest, filepath.ToSlash(rel))
	_, err = backup.Stage(t.Context(), backup.StageOpts{ConfigDir: config, DBPath: opts.DBPath, SQLDB: database, ArchivePath: archivePath(t, incomplete), SchemaVersion: db.SchemaVersion})
	if !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("restore accepted missing journal: %v", err)
	}
	if _, err := os.Stat(backup.PendingMarkerPath(config)); !os.IsNotExist(err) {
		t.Fatalf("invalid restore published: %v", err)
	}
}
