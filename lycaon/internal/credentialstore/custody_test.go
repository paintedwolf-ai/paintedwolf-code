package credentialstore

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIsolatedProcessesReportDevelopmentCustody(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	report, err := DescribeCustody(path)
	testutil.FailErr(t, "describe", err)
	if report.Mode != CustodyDevelopment || report.ServiceVerified {
		t.Fatalf("report = %+v", report)
	}
	if (unavailableIdentityProvider{}).Describe() == "" {
		t.Fatal("unavailable identity provider has no description")
	}
}
