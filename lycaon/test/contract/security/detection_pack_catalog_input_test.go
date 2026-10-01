package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/detectionpack"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The app reaches shipped detection packs as resolved catalog units. These
// contracts are about the merge and the import floor rather than about resolve,
// so they gather the same bytes straight from the tree; that the catalog reaches
// identical packs is pinned by the extension-side parity test.

func shippedDetectionPacks(t *testing.T) []detectionpack.Pack {
	t.Helper()
	packs, warnings, err := detectionpack.ShippedPacks()
	contractcheck.FailErr(t, "gather shipped detection packs", err)
	if len(warnings) > 0 {
		t.Fatalf("shipped detection packs loaded with warnings: %v", warnings)
	}
	return packs
}

func detectionInput(t *testing.T, configDir, projectDir string) detectionpack.Input {
	t.Helper()
	return detectionpack.Input{
		ConfigDir:   configDir,
		ProjectDir:  projectDir,
		Contributed: shippedDetectionPacks(t),
	}
}
