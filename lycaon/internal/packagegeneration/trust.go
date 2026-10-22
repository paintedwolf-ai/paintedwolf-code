//go:build linux

package packagegeneration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// layout fixes where generations and lifetime locks live and which owner they trust.
// Every directory from a store path up to anchor must be controlled; the native
// layout anchors at "/" so the whole ancestry is checked.
type layout struct {
	store, locks, anchor string
	owner                uint32
}

var native = layout{store: nativeStore, locks: lifetimeLocks, anchor: "/", owner: 0}

// controlledParents validates existing ancestors before creating store-owned leaves.
func (l layout) controlledParents(name string) error {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return fmt.Errorf("native generation: unclean store path")
	}
	if relative, err := filepath.Rel(l.anchor, name); err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return fmt.Errorf("native generation: store path %q outside trust anchor", name)
	}
	for path := name; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !info.IsDir() || !ok || stat.Uid != 0 && stat.Uid != l.owner || info.Mode().Perm()&0022 != 0 {
				return fmt.Errorf("native generation: uncontrolled store ancestor %q: mode %v", path, info.Mode())
			}
		}
		if path == l.anchor {
			return nil
		}
	}
}

func controlledObject(name string, info os.FileInfo, owner uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("native generation: uncontrolled payload object %q: no ownership facts", name)
	}
	if stat.Uid != owner || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("native generation: uncontrolled payload object %q: uid %d mode %v", name, stat.Uid, info.Mode())
	}
	return nil
}
