package confine_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWriteRootsForProjectMatchesBuildProfile(t *testing.T) {
	proj := t.TempDir()
	t.Setenv("LYCAON_SANDBOX_WRITE_ROOTS", "/opt/customcache-1451")
	roots := confine.WriteRootsForProject("", []string{proj})
	profile, err := confine.BuildProfile(confine.Confinement{Roots: []string{proj}})
	testutil.FailErr(t, "BuildProfile", err)
	// Scoped to the write-allow block: a root that appears only in a deny block is not
	// a write root, and an unscoped Contains cannot tell the two apart.
	allowWrite, _ := profileBlock(t, profile, blockAllowWrite, 0)
	for _, r := range roots {
		assertBlockCoversPath(t, allowWrite, r, "write-root allow")
	}
	assertBlockCoversPath(t, allowWrite, "/opt/customcache-1451", "LYCAON_SANDBOX_WRITE_ROOTS extra")
}

func TestPathWithinWriteRootsSymlinkParity(t *testing.T) {
	proj := t.TempDir()
	roots := confine.WriteRootsForProject("", []string{proj})
	tmpProbe := filepath.Join(os.TempDir(), "lycaon-1451-probe")
	if !confine.PathWithinWriteRoots(tmpProbe, roots) {
		t.Fatalf("os.TempDir child must be inside write roots: path=%q roots=%v", tmpProbe, roots)
	}
	if runtime.GOOS == "darwin" {
		if !confine.PathWithinWriteRoots("/tmp/lycaon-1451-probe", roots) {
			t.Fatalf("darwin /tmp must resolve into write roots (seatbelt parity): roots=%v", roots)
		}
	}
	if confine.PathWithinWriteRoots("/etc/hosts", roots) {
		t.Fatal("/etc/hosts must not be inside default write roots")
	}
}
