package oar

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

// ensureCatalog installs the bundled anchor catalog for LoadDir tests.
// Does not Clear — other tests in the package may share process catalog state.
func ensureCatalog(t *testing.T) {
	t.Helper()
	path := filepath.Join(testutil.CheckoutRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")
	testutil.FailErr(t, "install catalog", anchorcatalog.InstallFile(path))
	// A core anchor only resolves through the host's capability document
	// ([OAR-PROF-4]), so a test that loads rules needs it installed too.
	profile := filepath.Join(testutil.CheckoutRoot(t), "lycaon", "config", "packs", "painted-wolf",
		"platform", "host", "anchors", "oar-profile.yaml")
	testutil.FailErr(t, "install capability document", InstallCapabilityDocumentFile(profile))
}
