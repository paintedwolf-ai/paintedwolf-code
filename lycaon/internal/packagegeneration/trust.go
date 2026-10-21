//go:build linux

package packagegeneration

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// controlledParents validates existing ancestors before creating store-owned leaves.
func controlledParents(name string, owner uint32) error {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return fmt.Errorf("native generation: unclean store path")
	}
	for path := name; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		// Root's sticky temporary parent is permitted for isolated tests only.
		sticky := owner != 0 && path == os.TempDir() && info.Mode()&os.ModeSticky != 0
		if !info.IsDir() || !ok || (!sticky && (stat.Uid != 0 && stat.Uid != owner || info.Mode().Perm()&0022 != 0)) {
			return fmt.Errorf("native generation: uncontrolled store ancestor %q", path)
		}
		if path == filepath.Dir(path) {
			break
		}
	}
	return nil
}

func controlledObject(info os.FileInfo, owner uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != owner || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("native generation: uncontrolled payload object")
	}
	return nil
}
