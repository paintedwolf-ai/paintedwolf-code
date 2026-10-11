//go:build darwin

package fseffect

import (
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fspath"
	"golang.org/x/sys/unix"
)

// RestoreNativeTrash consumes an exact OS-returned Trash location. macOS permits
// relocating that entry while denying directory handles to the private Trash.
// The destination stays descriptor-relative, jailed, and exclusive.
func RestoreNativeTrash(source string, destination Location, expected string) error {
	if !filepath.IsAbs(source) || expected == "" {
		return fmt.Errorf("invalid native Trash receipt")
	}
	parent, err := openParent(destination, true, 0755)
	if err != nil {
		return err
	}
	defer parent.close()
	identity, err := fspath.EntryIdentity(source)
	if err != nil {
		return err
	}
	if identity != expected {
		return ErrPostcondition
	}
	if err := renameNoReplace(unix.AT_FDCWD, source, int(parent.file.Fd()), parent.name); err != nil {
		return err
	}
	return fsyncFD(int(parent.file.Fd()))
}
