package db

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRetainedFastFailureAfterCapturePreservesCustodyAndRetry(t *testing.T) {
	for _, outcome := range []string{"capture I/O failure", "cancellation after capture"} {
		t.Run(outcome, func(t *testing.T) {
			source, _ := completeRetainedFixture(t)
			before, err := os.ReadFile(source)
			testutil.FailErr(t, "read immutable original", err)
			recovery := filepath.Join(t.TempDir(), "recovery.db")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			captured, committed, ready := false, false, false
			var recoveryBefore []byte
			hooks := UpgradeHooks{
				Before: func(ctx context.Context, reader UpgradeSource, _ migrations.Plan) error {
					var path string
					err := reader.DB.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path)
					testutil.FailErr(t, "locate private capture source", err)
					cloned, err := CloneSnapshot(ctx, reader.DB, path, recovery)
					testutil.FailErr(t, "clone canonical recovery where supported", err)
					if !cloned {
						_, err = CopyQuiescentSnapshot(ctx, reader.DB, path, recovery)
						testutil.FailErr(t, "copy canonical recovery otherwise", err)
					}
					captured = true
					verifyFastCustodyImage(t, recovery, 4)
					recoveryBefore, err = os.ReadFile(recovery)
					testutil.FailErr(t, "retain verified recovery bytes", err)
					if outcome == "capture I/O failure" {
						return syscall.EIO
					}
					cancel()
					return nil
				},
				After: func(context.Context, migrations.Plan) error {
					committed = true
					return nil
				},
				Progress: func(phase migrations.Phase) { ready = ready || phase == migrations.Ready },
			}
			err = upgradeExisting(ctx, source, hooks, false)
			cause := error(syscall.EIO)
			if outcome == "cancellation after capture" {
				cause = context.Canceled
			}
			if !errors.Is(err, cause) {
				t.Fatalf("upgrade failure=%v, want retained cause %v", err, cause)
			}
			if !captured || committed || ready {
				t.Fatalf("failed custody transition: captured=%v committed=%v ready=%v", captured, committed, ready)
			}
			assertFastCustodyBytes(t, source, before)
			assertFastCustodyBytes(t, recovery, recoveryBefore)
			verifyFastCustodyImage(t, source, 4)
			verifyFastCustodyImage(t, recovery, 4)
			assertFastCustodyCleanup(t, source)
			testutil.FailErr(t, "retry immutable original with fresh context", UpgradeStaged(t.Context(), source))
			verifyFastCustodyImage(t, source, SchemaVersion)
			assertFastCustodyBytes(t, recovery, recoveryBefore)
			verifyFastCustodyImage(t, recovery, 4)
			assertFastCustodyCleanup(t, source)
		})
	}
}

func assertFastCustodyBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	testutil.FailErr(t, "read retained custody bytes", err)
	if !bytes.Equal(got, want) {
		t.Fatalf("private upgrade changed retained image %s", filepath.Base(path))
	}
}

func verifyFastCustodyImage(t *testing.T, path string, revision int) {
	t.Helper()
	database, err := openReader(t.Context(), path)
	testutil.FailErr(t, "open retained custody image", err)
	defer func() { _ = database.Close() }()
	testutil.FailErr(t, "verify retained custody integrity and foreign keys", auditSnapshot(t.Context(), database, nil))
	got, err := ReadUserVersion(t.Context(), database)
	testutil.FailErr(t, "read retained custody revision", err)
	if got != revision {
		t.Fatalf("retained image revision=%d, want %d", got, revision)
	}
}

func assertFastCustodyCleanup(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(rebuildMarker(path)); !os.IsNotExist(err) {
		t.Fatalf("completed refusal retained its building journal: %v", err)
	}
	work, err := filepath.Glob(filepath.Join(filepath.Dir(path), StoreUpgradeDirectoryPrefix+"*"))
	testutil.FailErr(t, "inspect disposable work cleanup", err)
	if len(work) != 0 {
		t.Fatalf("private work escaped cleanup: %v", work)
	}
}
