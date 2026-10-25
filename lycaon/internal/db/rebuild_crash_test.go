package db

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRebuildCrashKeepsLivePath(t *testing.T) {
	if path := os.Getenv("PW_REBUILD_CRASH_FIXTURE"); path != "" {
		record, err := readRebuildJournal(path)
		testutil.FailErr(t, "read child journal", err)
		_ = finishRebuildPublication(path, record, func(string) error { os.Exit(73); return nil })
		os.Exit(74)
	}
	path, record := preparedRebuildFixture(t)
	child := exec.Command(os.Args[0], "-test.run=^TestRebuildCrashKeepsLivePath$")
	child.Env = append(os.Environ(), "PW_REBUILD_CRASH_FIXTURE="+path)
	var stopped *exec.ExitError
	if err := child.Run(); !errors.As(err, &stopped) || stopped.ExitCode() != 73 {
		t.Fatalf("child interruption: %v", err)
	}
	testutil.FailErr(t, "live path already names replacement", requireRebuildIdentity(path, record.ReplacementIdentity))
	testutil.FailErr(t, "resume interrupted exchange", ResumeStoreUpgrade(path))
	testutil.FailErr(t, "preserve original after exchange", requireRebuildIdentity(filepath.Join(filepath.Dir(path), record.Directory, "original.db"), record.OriginalIdentity))
}

func TestRebuildRestartPreservesOlderBuildWritesBeforeExchange(t *testing.T) {
	source, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 3})
	record, err := beginRebuild(source)
	testutil.FailErr(t, "begin replacement", err)
	replacement := filepath.Join(filepath.Dir(source), record.Directory, "replacement.db")
	original, err := os.ReadFile(source)
	testutil.FailErr(t, "read released source", err)
	testutil.FailErr(t, "seed unpublished replacement", os.WriteFile(replacement, original, 0600))
	testutil.FailErr(t, "seal original", sealRebuiltStore(t.Context(), source))
	testutil.FailErr(t, "prepare exchange", prepareRebuildPublication(source, replacement, &record))
	// An older build still sees its released schema and may write after the crash.
	older, err := sql.Open("sqlite", source)
	testutil.FailErr(t, "open original after interruption", err)
	_, err = older.Exec(`INSERT INTO store_meta(key,value) VALUES('older-build-retained','new work')`)
	testutil.FailErr(t, "retain work in older build", err)
	testutil.FailErr(t, "close older build", older.Close())
	testutil.FailErr(t, "discard stale unpublished candidate", ResumeStoreUpgrade(source))
	older, err = sql.Open("sqlite", source)
	testutil.FailErr(t, "read resumed source", err)
	defer older.Close()
	var value string
	testutil.FailErr(t, "read older work", older.QueryRow(`SELECT value FROM store_meta WHERE key='older-build-retained'`).Scan(&value))
	if value != "new work" {
		t.Fatal("older build work lost")
	}
	if _, err := os.Stat(rebuildMarker(source)); !os.IsNotExist(err) {
		t.Fatalf("stale candidate still publishable: %v", err)
	}
}

func TestRebuildSealPreservesCommittedWALAfterProcessExit(t *testing.T) {
	if path := os.Getenv("PW_REBUILD_WAL_FIXTURE"); path != "" {
		writer, err := sql.Open("sqlite", path)
		testutil.FailErr(t, "open WAL child", err)
		_, err = writer.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE retained(value TEXT); INSERT INTO retained VALUES('committed')`)
		testutil.FailErr(t, "commit WAL child data", err)
		os.Exit(73)
	}
	path := filepath.Join(t.TempDir(), "store.db")
	child := exec.Command(os.Args[0], "-test.run=^TestRebuildSealPreservesCommittedWALAfterProcessExit$")
	child.Env = append(os.Environ(), "PW_REBUILD_WAL_FIXTURE="+path)
	var stopped *exec.ExitError
	if err := child.Run(); !errors.As(err, &stopped) || stopped.ExitCode() != 73 {
		t.Fatalf("WAL child interruption: %v", err)
	}
	info, err := os.Stat(path + "-wal")
	testutil.FailErr(t, "inspect committed WAL", err)
	if info.Size() == 0 {
		t.Fatal("fixture left no committed WAL")
	}
	testutil.FailErr(t, "seal committed WAL", sealRebuiltStore(t.Context(), path))
	testutil.FailErr(t, "require independent database", requireSealedRebuild(path))
	reader, err := sql.Open("sqlite", fileDSN(path, "mode=ro"))
	testutil.FailErr(t, "open sealed database", err)
	defer reader.Close()
	var value string
	testutil.FailErr(t, "read committed data without WAL", reader.QueryRow(`SELECT value FROM retained`).Scan(&value))
	if value != "committed" {
		t.Fatal("sealing lost committed WAL data")
	}
}
