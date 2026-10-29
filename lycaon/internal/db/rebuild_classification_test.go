package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

// This contract is carried by the combined source until the classifier prerequisite lands.
func TestRebuildRecoveryFailureUsesOperationalClassification(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		reason RecoveryReason
	}{
		{syscall.ENOSPC, RecoveryReasonInsufficientSpace},
		{syscall.EIO, RecoveryReasonStorageUnavailable},
		{syscall.EACCES, RecoveryReasonStorageUnavailable},
	} {
		t.Run(string(tc.reason)+tc.cause.Error(), func(t *testing.T) {
			source, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 3})
			invoked := false
			store, err := OpenWithOptions(t.Context(), source, UpgradeHooks{Before: func(context.Context, UpgradeSource, migrations.Plan) error { invoked = true; return tc.cause }}, StoreOptions{FastTestSync: true})
			if store != nil {
				_ = store.Close()
				t.Fatal("opened despite failed recovery capture")
			}
			var failure *StoreIncompatibleError
			if !invoked || !errors.As(err, &failure) || failure.Reason != tc.reason {
				t.Fatalf("rebuild failure = %v, want %s (capture reached=%v)", err, tc.reason, invoked)
			}
			reader, err := OpenReadOnly(t.Context(), source)
			testutil.FailErr(t, "open unchanged source", err)
			defer reader.Close()
			plan, err := PlanUpgrade(t.Context(), reader)
			testutil.FailErr(t, "plan unchanged source", err)
			if plan.Source.Revision != 4 {
				t.Fatalf("failure published revision %d", plan.Source.Revision)
			}
		})
	}
}

func TestRebuildAllocationAndSealUseOpenClassification(t *testing.T) {
	for _, tc := range []struct {
		phase  string
		cause  error
		reason RecoveryReason
	}{
		{"copy", nil, RecoveryReasonInsufficientSpace},
		{"seal", syscall.ENOSPC, RecoveryReasonInsufficientSpace},
		{"seal", syscall.EACCES, RecoveryReasonStorageUnavailable},
		{"seal", syscall.EIO, RecoveryReasonStorageUnavailable},
	} {
		t.Run(tc.phase+fmt.Sprint(tc.cause), func(t *testing.T) {
			source, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 3})
			testutil.FailErr(t, "seal byte-comparison baseline", sealRebuiltStore(t.Context(), source))
			before, err := os.ReadFile(source)
			testutil.FailErr(t, "read original", err)
			injectRebuildFault(t, source, tc.phase, tc.cause)
			store, err := OpenWithOptions(t.Context(), source, UpgradeHooks{Before: func(context.Context, UpgradeSource, migrations.Plan) error { return nil }}, StoreOptions{FastTestSync: true})
			if store != nil {
				_ = store.Close()
				t.Fatal("opened after failed copy/seal")
			}
			var failure *StoreIncompatibleError
			if !errors.As(err, &failure) || failure.Reason != tc.reason {
				t.Fatalf("open classification=%v, want %s", err, tc.reason)
			}
			after, err := os.ReadFile(source)
			testutil.FailErr(t, "read refused original", err)
			if !bytes.Equal(before, after) {
				t.Fatal("failed open changed original")
			}
			if _, err := os.Stat(rebuildMarker(source)); !os.IsNotExist(err) {
				t.Fatalf("failed open left journal: %v", err)
			}
			work, err := filepath.Glob(filepath.Join(filepath.Dir(source), StoreUpgradeDirectoryPrefix+"*"))
			testutil.FailErr(t, "inspect failed work", err)
			if len(work) != 0 {
				t.Fatal("failed open left unpublished work")
			}
		})
	}
}
