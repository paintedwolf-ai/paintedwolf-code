//go:build !linux

package credentialstore

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSingleCustodianPlatformsReportTheirCustody(t *testing.T) {
	report, err := describeCustody("/cfg/credential-vault.age")
	testutil.FailErr(t, "describe", err)
	if report.Mode == "" || report.ServiceVerified {
		t.Fatalf("report = %+v", report)
	}
}
