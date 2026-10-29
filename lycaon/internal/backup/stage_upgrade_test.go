package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStagedInspectionPreservesOperationalFailures(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, syscall.ENOSPC, os.ErrPermission} {
		result := stagedInspectionFailure(cause, &Manifest{}, "inspect extracted history")
		if errors.Is(result, ErrInvalid) || errors.Is(result, ErrBaselineMismatch) {
			t.Fatalf("operational failure labeled invalid: %v", result)
		}
		if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
			if !errors.Is(result, cause) {
				t.Fatalf("lost cancellation: %v", result)
			}
		} else {
			var failure *db.StoreIncompatibleError
			if !errors.As(result, &failure) {
				t.Fatalf("lost operational classification: %v", result)
			}
		}
	}
}

func TestCanceledStagedInspectionLeavesSourceUntouched(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	store := testdbfixture.OpenPath(t, path)
	testutil.FailErr(t, "close staged database", store.Close())
	before, err := os.ReadFile(path)
	testutil.FailErr(t, "read source before cancellation", err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = upgradeStagedStore(ctx, root, &Manifest{SchemaUserVersion: db.SchemaVersion}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled staging = %v", err)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read source after cancellation", err)
	if string(before) != string(after) {
		t.Fatal("canceled inspection changed source")
	}
}

func TestCurrentSchemaRestoreSealsAuditObligationAndRefreshesItsManifest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	store := testdbfixture.OpenPath(t, path)
	testutil.FailErr(t, "close clean archive source", store.Close())
	before, size, err := sourceDigest(t.Context(), archiveSource{path: path, kind: fileKindRegular})
	testutil.FailErr(t, "authenticate clean archive source", err)
	baseline, err := db.CurrentBaseline(t.Context())
	testutil.FailErr(t, "identify current archive schema", err)
	manifest := Manifest{SchemaUserVersion: baseline.Revision, SchemaShapeDigest: baseline.Shape,
		Files: []FileEntry{{RelPath: storeRelPath, Kind: fileKindRegular, SHA256: before, Size: size}}}
	files := []PendingFile{{RelPath: storeRelPath, Kind: fileKindRegular, SHA256: before, Size: size}}
	testutil.FailErr(t, "stage current schema with deferred audit", upgradeStagedStore(t.Context(), root, &manifest, files))
	after, length, err := sourceDigest(t.Context(), archiveSource{path: path, kind: fileKindRegular})
	testutil.FailErr(t, "authenticate staged obligation", err)
	if after == before || manifest.Files[0].SHA256 != after || files[0].SHA256 != after || manifest.Files[0].Size != length || files[0].Size != length {
		t.Fatal("current-schema staged bytes lost their updated integrity manifest")
	}
	reader, err := db.OpenReadOnly(t.Context(), path)
	testutil.FailErr(t, "open sealed staged obligation", err)
	defer reader.Close()
	var state string
	testutil.FailErr(t, "read restored audit obligation", reader.QueryRowContext(t.Context(), "SELECT value FROM store_meta WHERE key='integrity_audit_failed'").Scan(&state))
	if state != "integrity audit pending" {
		t.Fatal("clean archive skipped deferred verification after activation")
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		if info, err := os.Stat(path + suffix); err == nil && info.Size() > 0 {
			t.Fatalf("staged obligation depends on unpublished sidecar %s", suffix)
		} else if err != nil && !os.IsNotExist(err) {
			testutil.FailErr(t, "inspect sealed staging", err)
		}
	}
}

func TestStagedInspectionKeepsConfirmedInvalidity(t *testing.T) {
	for _, tc := range []struct {
		reason db.RecoveryReason
		want   error
	}{
		{db.RecoveryReasonIntegrityFailed, ErrInvalid},
		{db.RecoveryReasonSchemaMismatch, ErrBaselineMismatch},
	} {
		err := stagedInspectionFailure(&db.StoreIncompatibleError{Reason: tc.reason}, &Manifest{}, "inspect extracted history")
		if !errors.Is(err, tc.want) {
			t.Fatalf("classification %s = %v", tc.reason, err)
		}
	}
}

func TestRestoreApplyPreservesOperationalFailures(t *testing.T) {
	for _, cause := range []error{context.Canceled, syscall.ENOSPC, syscall.EIO, syscall.EROFS, os.ErrPermission} {
		err := unapplicableRestore(cause)
		if errors.Is(cause, context.Canceled) {
			if !errors.Is(err, cause) {
				t.Fatal("restore lost cancellation")
			}
			continue
		}
		var failure *db.StoreIncompatibleError
		if !errors.As(err, &failure) || failure.Reason == db.RecoveryReasonIntegrityFailed {
			t.Fatalf("operational apply failure labeled damaged history: %v", err)
		}
	}
}
