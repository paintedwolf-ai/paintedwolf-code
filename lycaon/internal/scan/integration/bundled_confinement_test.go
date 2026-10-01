package integration

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/scan"
)

func TestBundledScannerConfinementDeniesNetwork(t *testing.T) {
	if !confine.Available() {
		t.Skip("OS confinement unavailable")
	}
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	projectDir := t.TempDir()
	outputDir := t.TempDir()
	box, err := scan.BundledScannerConfinement(projectDir, outputDir)
	if err != nil {
		t.Fatalf("BundledScannerConfinement: %v", err)
	}
	if box == nil || box.Network != confine.NetworkDeny || box.ProxyAddr != "" || box.LineageID != "" {
		t.Fatalf("bundled scanner confinement = %+v, want offline boundary", box)
	}
	if !reflect.DeepEqual(box.Roots, []string{outputDir}) {
		t.Fatalf("bundled scanner write roots = %v, want only %q", box.Roots, outputDir)
	}
	if !reflect.DeepEqual(box.ReadRoots, []string{projectDir}) {
		t.Fatalf("bundled scanner read roots = %v, want only %q", box.ReadRoots, projectDir)
	}
}
