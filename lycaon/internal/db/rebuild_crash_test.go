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

func TestRebuildCrashWithUnawareEmptyOpener(t *testing.T) {
	if path := os.Getenv("PW_REBUILD_CRASH_FIXTURE"); path != "" {
		record, err := readRebuildJournal(path)
		testutil.FailErr(t, "read child journal", err)
		moves := 0
		_ = finishRebuildPublication(path, record, func(string) error {
			moves++
			if moves == 4 {
				os.Exit(73)
			}
			return nil
		})
		os.Exit(74)
	}
	path, record := preparedRebuildFixture(t)
	child := exec.Command(os.Args[0], "-test.run=^TestRebuildCrashWithUnawareEmptyOpener$")
	child.Env = append(os.Environ(), "PW_REBUILD_CRASH_FIXTURE="+path)
	var stopped *exec.ExitError
	if err := child.Run(); !errors.As(err, &stopped) || stopped.ExitCode() != 73 {
		t.Fatalf("child interruption: %v", err)
	}
	// Simulate an older opener that knows nothing about the publication journal.
	unaware, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open absent path", err)
	_, err = unaware.Exec(`PRAGMA user_version=0`)
	testutil.FailErr(t, "initialize empty database", err)
	testutil.FailErr(t, "close unaware opener", unaware.Close())
	testutil.FailErr(t, "resume after unaware opener", ResumeStoreUpgrade(path))
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read replacement", err)
	if string(raw) != "verified replacement" {
		t.Fatal("replacement lost")
	}
	preserved, err := filepath.Glob(filepath.Join(filepath.Dir(path), record.Directory, "empty-occupant-*", "store.db"))
	testutil.FailErr(t, "find preserved occupant", err)
	if len(preserved) != 1 {
		t.Fatal("empty occupant was not preserved")
	}
}

func TestRebuildRefusesNewUserState(t *testing.T) {
	path, record := preparedRebuildFixture(t)
	moves := 0
	_ = finishRebuildPublication(path, record, func(string) error {
		moves++
		if moves == 4 {
			return errors.New("interrupted")
		}
		return nil
	})
	occupant, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open new occupant", err)
	_, err = occupant.Exec(`CREATE TABLE user_data(value TEXT); INSERT INTO user_data VALUES('keep')`)
	testutil.FailErr(t, "write new user state", err)
	testutil.FailErr(t, "close occupant", occupant.Close())
	if err := ResumeStoreUpgrade(path); err == nil {
		t.Fatal("overwrote new user state")
	}
}
