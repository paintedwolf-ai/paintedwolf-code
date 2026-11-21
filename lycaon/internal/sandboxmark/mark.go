// Package sandboxmark finds the processes still running under this engine's
// sandbox profiles. A sandbox outlives fork, exec, setsid and closed
// descriptors, which is how a daemon escapes every other attribution on macOS.
// Every profile the engine renders lets its processes write one marker file and
// not its sibling; no other profile does both, because both live in the
// control-plane tree that profiles deny unless they name it.
package sandboxmark

import (
	"os"
	"path/filepath"
)

const (
	ownName     = "own"
	foreignName = "foreign"
)

// Remove deletes a mark's markers once nothing can run under them.
func Remove(dir string) {
	if !filepath.IsAbs(dir) {
		return
	}
	_ = os.Remove(filepath.Join(dir, ownName))
	_ = os.Remove(filepath.Join(dir, foreignName))
	_ = os.Remove(dir)
	// The parent goes only once no other engine's mark remains in it.
	_ = os.Remove(filepath.Dir(dir))
}
