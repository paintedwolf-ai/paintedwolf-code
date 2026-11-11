//go:build darwin && cgo

package socketref

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestVolumeTemporaryDirectoryFollowsTheRequestedFilesystem(t *testing.T) {
	source := endpointFixtureRoot(t)
	root, err := volumeTemporaryDirectory(source)
	testutil.FailErr(t, "select service-volume temporary directory", err)
	defer os.Remove(root)
	var service, temporary unix.Stat_t
	testutil.FailErr(t, "inspect service filesystem", unix.Stat(source, &service))
	testutil.FailErr(t, "inspect temporary filesystem", unix.Stat(root, &temporary))
	if service.Dev != temporary.Dev || temporary.Uid != uint32(os.Getuid()) {
		t.Fatalf("temporary directory has wrong filesystem or user: service=%d, temporary=%d, uid=%d", service.Dev, temporary.Dev, temporary.Uid)
	}
	if filepath.Clean(root) == filepath.Clean(source) {
		t.Fatal("temporary selection reused the service directory")
	}
}
