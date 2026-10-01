package detectionpack

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The running app receives shipped packs as resolved catalog units. These tests
// exercise the same merge with the same bytes, gathered straight from the tree:
// what rehearses here is what ships, and that the catalog reaches identical
// packs is pinned separately by the extension-side contract test.

// shippedPacks gathers every detection pack in the binary's tree.
func shippedPacks(t *testing.T) []Pack {
	t.Helper()
	packs, warnings, err := ShippedPacks()
	if err != nil {
		testutil.FailErr(t, "gather shipped detection packs", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("shipped detection packs loaded with warnings: %v", warnings)
	}
	return packs
}

// deviceInput is one catalog load over the shipped packs plus device state.
func deviceInput(t *testing.T, configDir, projectDir string) Input {
	t.Helper()
	return Input{
		ConfigDir:   configDir,
		ProjectDir:  projectDir,
		Contributed: shippedPacks(t),
	}
}

// shippedInput is deviceInput with no device or project state.
func shippedInput(t *testing.T) Input {
	t.Helper()
	return deviceInput(t, "", "")
}
