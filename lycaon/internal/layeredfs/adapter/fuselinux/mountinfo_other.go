//go:build !linux

package fuselinux

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

type kernelMountEntry struct {
	mountID       string
	devMajorMinor string
	mountPoint    string
	mountOptions  string
	filesystem    string
	source        string
	superOptions  string
}

func statDeviceID(_ string) (uint64, error) {
	return 0, fmt.Errorf("fuselinux: statDeviceID requires linux")
}

func checkFUSEDeviceAvailable(_ string) error {
	return fmt.Errorf("fuselinux: /dev/fuse requires linux")
}

func closeRawFD(_ int) {}

func verifyKernelMountInfo(_ string, _ string, _ uint32, _ uint32) (kernelMountEntry, error) {
	return kernelMountEntry{}, fmt.Errorf("fuselinux: mountinfo requires linux")
}

func checkKernelMountTable(_, _, _ string) (bool, bool) {
	return false, false
}

func findRecordedMount(adapter.RecordedConnection) (kernelMountEntry, recordedMountState, error) {
	return kernelMountEntry{}, recordedMountAbsent, fmt.Errorf("fuselinux: mountinfo requires linux")
}

func connectionEnded(context.Context, string) bool { return false }

func servingProcessEnded(adapter.ProcessIdentity) bool { return false }

func abortConnection(string) error {
	return fmt.Errorf("fuselinux: connection abort requires linux")
}

func mountPathsWithin(_ string) ([]string, error) {
	return nil, fmt.Errorf("fuselinux: mountinfo requires linux")
}

func syncMountedFilesystem(_ string) error {
	return fmt.Errorf("fuselinux: syncfs requires linux")
}

func canonicalPath(path string) string {
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	return clean
}

func mountAndStartFUSEServer(_ context.Context, _ Config, _ ServerConfig, _ *layeredfs.Core) (*Server, error) {
	return nil, fmt.Errorf("fuselinux: FUSE server requires linux")
}

func (s *Server) optOutOfFUSEPoll(_ string) error {
	return fmt.Errorf("fuselinux: FUSE server requires linux")
}

func notifyInvalInode(_ *Server, _ uint64) {}

func notifyInvalEntry(_ *Server, _ uint64, _ string) {}
