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
	"sync"
)

const (
	ownName     = "own"
	foreignName = "foreign"
)

var current struct {
	mu  sync.Mutex
	dir string
}

// OwnPath is the marker every profile admits, or empty while no mark is enabled.
func OwnPath() string {
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.dir == "" {
		return ""
	}
	return filepath.Join(current.dir, ownName)
}

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
