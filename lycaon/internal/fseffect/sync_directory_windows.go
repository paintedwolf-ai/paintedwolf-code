package fseffect

import (
	"os"

	"github.com/lycaon/lycaon/internal/fssync"
	"golang.org/x/sys/windows"
)

// flushHandle flushes a held handle under the process durability policy.
func flushHandle(handle windows.Handle) error {
	if fssync.Relaxed() {
		return nil
	}
	return windows.FlushFileBuffers(handle)
}

func SyncDirectory(root *os.Root, path string) error {
	parent, err := openWindowsParent(Location{Root: root.Name(), Rel: path}, false, true)
	if err != nil {
		return err
	}
	defer parent.close()
	handle, err := ntOpenRelative(parent.handle, parent.name, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	return flushHandle(handle)
}
