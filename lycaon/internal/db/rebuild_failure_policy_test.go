package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRebuildFallbackRequiresEveryCauseToBeIntegrityDamage(t *testing.T) {
	damage := []error{migrations.ErrIntegrity, &StoreIncompatibleError{Reason: RecoveryReasonIntegrityFailed}, sqliteDamageForPolicy(t, false), sqliteDamageForPolicy(t, true)}
	operational := []error{context.Canceled, context.DeadlineExceeded, syscall.ENOSPC, syscall.EIO, os.ErrPermission, &StoreIncompatibleError{Reason: RecoveryReasonInsufficientSpace}, errors.New("diagnostic integrity prose"), sqliteFullForPolicy(t)}
	for _, d := range damage {
		for _, wrapped := range []error{d, fmt.Errorf("wrapped: %w", d), errors.Join(d, d), fmt.Errorf("wrapped join: %w", errors.Join(d, d))} {
			if !rebuildIntegrityFailure(wrapped) {
				t.Fatalf("confirmed damage refused fallback: %v", wrapped)
			}
		}
		for _, op := range operational {
			for _, mixed := range []error{errors.Join(d, op), fmt.Errorf("wrapped mixed: %w", errors.Join(d, op)), errors.Join(fmt.Errorf("nested: %w", d), fmt.Errorf("operational: %w", op))} {
				if rebuildIntegrityFailure(mixed) {
					t.Fatalf("mixed operational cause authorized retry: %v", mixed)
				}
			}
		}
	}
	for _, op := range operational {
		if rebuildIntegrityFailure(op) {
			t.Fatalf("operational cause authorized retry: %v", op)
		}
	}
	if rebuildIntegrityFailure(nil) {
		t.Fatal("no error authorized repair")
	}
}

func sqliteDamageForPolicy(t *testing.T, notDatabase bool) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	if notDatabase {
		if err := os.WriteFile(path, []byte("not a database"), 0600); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	} else {
		database, err := sql.Open("sqlite", path)
		testutil.FailErr(t, "sql.Open failed", err)
		if _, err := database.Exec("CREATE TABLE data(value TEXT); INSERT INTO data VALUES('kept')"); err != nil {
			testutil.FailErr(t, "database.Exec failed", err)
		}
		var page, size int64
		if err := database.QueryRow("SELECT rootpage FROM sqlite_schema WHERE name='data'").Scan(&page); err != nil {
			testutil.FailErr(t, "database.QueryRow failed", err)
		}
		if err := database.QueryRow("PRAGMA page_size").Scan(&size); err != nil {
			testutil.FailErr(t, "database.QueryRow failed", err)
		}
		if err := database.Close(); err != nil {
			testutil.FailErr(t, "database.Close failed", err)
		}
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		testutil.FailErr(t, "os.OpenFile failed", err)
		if _, err := file.WriteAt(make([]byte, size), (page-1)*size); err != nil {
			testutil.FailErr(t, "file.WriteAt failed", err)
		}
		if err := file.Close(); err != nil {
			testutil.FailErr(t, "file.Close failed", err)
		}
	}
	database, err := sql.Open("sqlite", fileDSN(path, "mode=ro"))
	if err != nil {
		return err
	}
	defer database.Close()
	var value string
	if notDatabase {
		err = database.QueryRow("PRAGMA user_version").Scan(&value)
	} else {
		err = database.QueryRow("SELECT value FROM data").Scan(&value)
	}
	if err == nil {
		t.Fatal("fixture did not produce SQLite damage")
	}
	return err
}

func sqliteFullForPolicy(t *testing.T) error {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "sql.Open failed", err)
	defer database.Close()
	database.SetMaxOpenConns(1)
	if _, err := database.Exec("CREATE TABLE data(value BLOB)"); err != nil {
		testutil.FailErr(t, "database.Exec failed", err)
	}
	var pages uint64
	if err := database.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		testutil.FailErr(t, "database.QueryRow failed", err)
	}
	if _, err := database.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", pages)); err != nil {
		testutil.FailErr(t, "database.Exec failed", err)
	}
	_, err = database.Exec("INSERT INTO data VALUES(zeroblob(1048576))")
	if err == nil {
		t.Fatal("full fixture was not capped")
	}
	return err
}
