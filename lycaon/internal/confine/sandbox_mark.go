package confine

import (
	"os"
	"path/filepath"
)

const (
	markOwnName     = "own"
	markForeignName = "foreign"
)

// RemoveSandboxMark deletes a mark's markers once nothing can run under them.
func RemoveSandboxMark(dir string) {
	if !filepath.IsAbs(dir) {
		return
	}
	_ = os.Remove(filepath.Join(dir, markOwnName))
	_ = os.Remove(filepath.Join(dir, markForeignName))
	_ = os.Remove(dir)
	// The parent goes only once no other engine's mark remains in it.
	_ = os.Remove(filepath.Dir(dir))
}
