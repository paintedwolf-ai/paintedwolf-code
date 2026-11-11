//go:build !darwin

package socketref

import (
	"os"
	"path/filepath"
)

// ServiceDirectory makes a private directory for a socket the host serves to
// confined actions. Confined clients reach it through a retained object, so
// its location carries no authority.
func ServiceDirectory() (string, error) {
	root := os.TempDir()
	if runtime := filepath.Clean(os.Getenv("XDG_RUNTIME_DIR")); filepath.IsAbs(runtime) {
		if info, err := os.Stat(runtime); err == nil && info.IsDir() {
			root = runtime
		}
	}
	return os.MkdirTemp(root, "pw-service-")
}
