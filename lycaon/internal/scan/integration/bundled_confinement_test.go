package integration

import (
	"os"
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
	workspace, err := confine.NewActionWorkspace()
	if err != nil {
		t.Fatalf("create scanner workspace: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workspace.Path()) })
	outputDir := workspace.Path()
	box, err := scan.BundledScannerConfinement(projectDir, workspace)
	if err != nil {
		t.Fatalf("BundledScannerConfinement: %v", err)
	}
	if box == nil || box.Network != confine.NetworkDeny || box.ProxyAddr != "" || box.LineageID != "" {
		t.Fatalf("bundled scanner confinement = %+v, want offline boundary", box)
	}
	if box.Workspaces.Action != workspace || !reflect.DeepEqual(box.Roots, []string{outputDir}) {
		t.Fatalf("bundled scanner write roots = %v, want only %q", box.Roots, outputDir)
	}
	if !reflect.DeepEqual(box.ReadRoots, []string{projectDir}) {
		t.Fatalf("bundled scanner read roots = %v, want only %q", box.ReadRoots, projectDir)
	}
}
