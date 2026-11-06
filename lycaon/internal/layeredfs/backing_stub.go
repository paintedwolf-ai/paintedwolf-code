//go:build !darwin && !linux

package layeredfs

import (
	"io/fs"
	"os"
)

func openDirNoFollow(_ string) (*os.File, error) {
	return nil, ErrUnsupportedOperation
}

func mkdirBackingDir(path string, mode fs.FileMode) error {
	return os.MkdirAll(path, mode.Perm())
}

func createExclusiveBackingFile(_ int, _, _ string, _ fs.FileMode) (*os.File, error) {
	return nil, ErrUnsupportedOperation
}

func openBackingFileRW(_ int, _, _ string) (*os.File, error) {
	return nil, ErrUnsupportedOperation
}

func openBackingFileRO(_ int, _, _ string) (*os.File, error) {
	return nil, ErrUnsupportedOperation
}

func unlinkBackingFile(_ int, _ string) error {
	return ErrUnsupportedOperation
}

func renameBackingFile(_ int, _, _ string) error {
	return ErrUnsupportedOperation
}

func syncFD(_ int) error {
	return nil
}

func cloneFileIfSupported(_, _ string) (bool, error) {
	return false, nil
}

type osLowerProvider struct{}

func newOSLowerProvider(_ string, _ func(rel, comp string), _ func(rel string) error) (*osLowerProvider, error) {
	return nil, ErrUnsupportedOperation
}

func (p *osLowerProvider) close() error {
	return nil
}

func (p *osLowerProvider) Lstat(_ string) (LowerEntry, error) {
	return LowerEntry{}, ErrUnsupportedOperation
}

func (p *osLowerProvider) Readlink(_ string) (string, error) {
	return "", ErrUnsupportedOperation
}

func (p *osLowerProvider) OpenRead(_ string) (ReadSeekCloser, LowerEntry, error) {
	return nil, LowerEntry{}, ErrUnsupportedOperation
}

func (p *osLowerProvider) ReadDir(_ string) ([]LowerDirEntry, error) {
	return nil, ErrUnsupportedOperation
}

func (p *osLowerProvider) ListXattr(_ string) (map[string][]byte, error) {
	return nil, ErrUnsupportedOperation
}
