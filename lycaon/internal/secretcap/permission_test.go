package secretcap

import (
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

func TestInvocationPermissionIsExactAndDetached(t *testing.T) {
	var absent *Resolution
	recipient := secretmatch.Recipient{ID: "service", Label: "Service", Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService}
	if absent.UseCovered("one", recipient) || absent.RecipientApproved(recipient) {
		t.Fatal("absent resolution granted authority")
	}
	resolution := &Resolution{}
	fingerprints := []secretmatch.SecretFingerprint{"one"}
	recipients := []secretmatch.Recipient{recipient}
	resolution.ApproveRelease(Release{Fingerprints: fingerprints, Recipients: recipients})
	fingerprints[0] = "two"
	recipients[0].ID = "other"
	if !resolution.UseCovered("one", recipient) || resolution.UseCovered("two", recipient) {
		t.Fatal("permission mutated with caller input")
	}
	other := recipient
	other.Surface = secretmatch.SurfaceMCP
	if resolution.UseCovered("one", other) || (&Resolution{}).RecipientApproved(recipient) {
		t.Fatal("once permission escaped its surface or invocation")
	}
	resolution.ApproveLocalConnections([]uint16{8080})
	if !resolution.LocalConnectionsCovered([]uint16{8080}) || resolution.LocalConnectionsCovered(nil) || resolution.LocalConnectionsCovered([]uint16{8080, 9090}) {
		t.Fatal("local connection permission widened")
	}
}
