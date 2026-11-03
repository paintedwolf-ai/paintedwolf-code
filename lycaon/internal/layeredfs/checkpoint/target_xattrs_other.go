//go:build !darwin && !linux

package checkpoint

import (
	"github.com/lycaon/lycaon/internal/fseffect"
	"os"
)

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
