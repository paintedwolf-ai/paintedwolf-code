//go:build !linux

package credentialstore

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSingleCustodianPlatformsCannotMove(t *testing.T) {
	report, err := describeCustody("/cfg/credential-vault.age")
	testutil.FailErr(t, "describe", err)
	if report.Mode == "" || report.ServiceVerified {
		t.Fatalf("report = %+v", report)
	}
	if !errors.Is(moveToService("/cfg/credential-vault.age"), ErrCustodyUnsupported) ||
		!errors.Is(moveToAppPassword("/cfg/credential-vault.age"), ErrCustodyUnsupported) {
		t.Fatal("a single-custodian platform moved an identity")
	}
}
