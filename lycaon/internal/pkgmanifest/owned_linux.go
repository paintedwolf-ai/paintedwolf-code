//go:build linux

package pkgmanifest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// VerifyOwnedPayload binds every object to the caller's independently selected
// trusted owner as well as its complete content seal. Bootstrap callers use UID0.
func (m *Manifest) VerifyOwnedPayload(root *os.Root, owner uint32) error {
	if root == nil {
		return errors.New("native manifest: missing retained payload root")
	}
	if err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != owner || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("native manifest: untrusted payload owner or permissions for %q", name)
		}
		return nil
	}); err != nil {
		return err
	}
	return m.VerifyPayload(root)
}
