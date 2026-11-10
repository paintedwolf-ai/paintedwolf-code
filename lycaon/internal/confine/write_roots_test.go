package confine_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWriteRootsForBoundaryMatchesBuildSeatbeltProfile(t *testing.T) {
	proj := t.TempDir()
	t.Setenv("LYCAON_SANDBOX_WRITE_ROOTS", "/opt/customcache-1451")
	roots, err := confine.WriteRootsForBoundary("", []string{proj}, nil, "")
	testutil.FailErr(t, "admit write roots", err)
	profile, err := confine.BuildSeatbeltProfile(confine.Confinement{Roots: []string{proj}})
	testutil.FailErr(t, "BuildSeatbeltProfile", err)
	// Scoped to the write-allow block: a root that appears only in a deny block is not
	// a write root, and an unscoped Contains cannot tell the two apart.
	allowWrite, _ := profileBlock(t, profile, blockAllowWrite, 0)
	for _, r := range roots {
		assertBlockCoversPath(t, allowWrite, r, "write-root allow")
	}
	assertBlockCoversPath(t, allowWrite, "/opt/customcache-1451", "LYCAON_SANDBOX_WRITE_ROOTS extra")
}

func TestPathWithinRootsSymlinkParity(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", "/opt/paintedwolf-fixture-state")
	proj := t.TempDir()
	roots, err := confine.WriteRootsForBoundary("", []string{proj}, nil, "")
	testutil.FailErr(t, "admit write roots", err)
	tmpProbe := filepath.Join(os.TempDir(), "lycaon-1451-probe")
	if !confine.PathWithinRoots(tmpProbe, roots) {
		t.Fatalf("os.TempDir child must be inside write roots: path=%q roots=%v", tmpProbe, roots)
	}
	if runtime.GOOS == "darwin" {
		if !confine.PathWithinRoots("/tmp/lycaon-1451-probe", roots) {
			t.Fatalf("darwin /tmp must resolve into write roots (seatbelt parity): roots=%v", roots)
		}
	}
	if confine.PathWithinRoots("/etc/hosts", roots) {
		t.Fatal("/etc/hosts must not be inside default write roots")
	}
}

func TestFilesystemRootRemainsInBoundaryAndKeepsControlPlaneFloor(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("LYCAON_CONFIG_DIR", state)
	roots, err := confine.WriteRootsForBoundary("", []string{"/"}, nil, "")
	testutil.FailErr(t, "admit filesystem root with floor protection", err)
	if !confine.PathWithinRoots("/etc/hosts", roots) {
		t.Fatalf("filesystem root missing from write roots: %v", roots)
	}
	c := confine.Confinement{Roots: []string{"/"}}
	fs := confine.BoundaryOf(&c).Filesystem
	if verdict := fs.Verdict(confine.AccessWrite, filepath.Join(state, "approvals.yaml")); verdict.Allowed || verdict.Layer != confine.FloorControlPlane {
		t.Fatalf("control-plane write verdict under / = %+v", verdict)
	}
	if verdict := fs.Verdict(confine.AccessWriteUnlink, filepath.Dir(state)); verdict.Allowed || verdict.Layer != confine.FloorControlPlane {
		t.Fatalf("control-plane ancestor unlink verdict under / = %+v", verdict)
	}
}
