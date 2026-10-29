//go:build linux

package documentcore

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxDocumentCoreAppliedBoundary(t *testing.T) {
	if !confine.Available() {
		caps, _ := confine.LinuxCapabilities()
		t.Fatalf("required Linux document boundary is unavailable: %+v", caps)
	}
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	verified, err := Verify(t.Context())
	testutil.FailErr(t, "verify applied native document core", err)
	if verified.Binary == "" || !verified.Confined {
		t.Fatalf("native document core ran without its required boundary: %+v", verified)
	}
}
