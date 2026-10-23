package confine_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWriteRootsForProjectMatchesBuildSeatbeltProfile(t *testing.T) {
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

func TestPathWithinWriteRootsSymlinkParity(t *testing.T) {
	proj := t.TempDir()
	roots, err := confine.WriteRootsForBoundary("", []string{proj}, nil, "")
	testutil.FailErr(t, "admit write roots", err)
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

func TestFilesystemRootCannotBecomeWriteAuthority(t *testing.T) {
	roots, err := confine.WriteRootsForBoundary("", []string{"/"}, nil, "")
	if err == nil || roots != nil {
		t.Fatalf("filesystem root admitted: roots=%v error=%v", roots, err)
	}
}
