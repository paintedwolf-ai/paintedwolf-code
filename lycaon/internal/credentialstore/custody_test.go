package credentialstore

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCustodyErrorCodesAreStable(t *testing.T) {
	for err, want := range map[error]string{
		nil:                    "ok",
		ErrVaultUnlockFailed:   "password_incorrect",
		ErrVaultLocked:         "password_required",
		ErrUnlockSecretInvalid: "password_invalid",
		ErrCustodyUnchanged:    "unchanged",
		ErrServiceUnverified:   "service_unavailable",
		ErrLauncherRefused:     "service_unavailable",
		ErrCustodyUnsupported:  "service_unavailable",
		ErrVaultCorrupt:        "failed",
	} {
		if got := CustodyErrorCode(err); got != want {
			t.Fatalf("CustodyErrorCode(%v) = %q, want %q", err, got, want)
		}
		if err != nil {
			if got := CustodyErrorCode(fmt.Errorf("wrapped: %w", err)); got != want {
				t.Fatalf("wrapped %v = %q", err, got)
			}
		}
	}
}

func TestIsolatedProcessesReportDevelopmentCustody(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	report, err := DescribeCustody(path)
	testutil.FailErr(t, "describe", err)
	if report.Mode != CustodyDevelopment {
		t.Fatalf("report = %+v", report)
	}
	if !errors.Is(MoveIdentityToService(path), ErrCustodyUnsupported) ||
		!errors.Is(MoveIdentityToAppPassword(path), ErrCustodyUnsupported) {
		t.Fatal("an isolated process moved a development identity")
	}
}
