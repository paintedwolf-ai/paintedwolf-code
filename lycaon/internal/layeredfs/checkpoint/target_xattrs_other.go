//go:build windows

package checkpoint

import (
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"golang.org/x/sys/windows"
)

func targetFileIdentity(file *os.File, info os.FileInfo) (string, error) {
	id, _, err := targetFileIdentityAndLinks(file, info)
	return id, err
}

func targetFileIdentityAndLinks(file *os.File, _ os.FileInfo) (string, uint64, error) {
	handle := windows.Handle(file.Fd())
	id, err := fspath.EntryIdentityHandle(handle)
	if err != nil {
		return "", 0, err
	}
	var bhfi windows.ByHandleFileInformation
	if infoErr := windows.GetFileInformationByHandle(handle, &bhfi); infoErr == nil {
		return id, uint64(bhfi.NumberOfLinks), nil
	}
	return id, 1, nil
}

func ApplyPathXattrs(_ string, attrs map[string][]byte) error {
	if len(attrs) != 0 {
		return fseffect.ErrUnsupported
	}
	return nil
}
func applyFileXattrs(_ *os.File, attrs map[string][]byte) error {
	if len(attrs) != 0 {
		return fseffect.ErrUnsupported
	}
	return nil
}

func readFileXattrs(_ *os.File) (map[string][]byte, error) { return nil, nil }
func restoreFileXattrs(file *os.File, attrs map[string][]byte) error {
	return applyFileXattrs(file, attrs)
}
