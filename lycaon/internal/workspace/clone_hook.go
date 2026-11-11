package workspace

import (
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// cloneWorkspaceFile replaces file cloning for every manager without its own cloner.
var cloneWorkspaceFile adapter.FileCloner

func (m *Manager) fileCloner() adapter.FileCloner {
	if m.cloneFile != nil {
		return m.cloneFile
	}
	return cloneWorkspaceFile
}

// UseCopyAsCloneForTest makes every clone succeed by copying. tmpfs has no reflink,
// so Linux lifecycle tests use it on a real second volume; byte-count tests do not.
func UseCopyAsCloneForTest() (restore func()) {
	previous := cloneWorkspaceFile
	cloneWorkspaceFile = func(src, dst string) (bool, error) {
		in, err := os.Open(src)
		if err != nil {
			return false, err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return false, err
		}
		_, copyErr := io.Copy(out, in)
		if closeErr := out.Close(); copyErr == nil {
			copyErr = closeErr
		}
		return true, copyErr
	}
	return func() { cloneWorkspaceFile = previous }
}
