package confine

import (
	"os"
	"path/filepath"
)

// packageCacheHomeRelRoots are home-relative package stores allowed as default
// write roots. Each names the store, not the tool home, because install and bin
// directories execute outside the sandbox.
var packageCacheHomeRelRoots = []string{
	".bun/install/cache",
	".npm/_cacache",
	".cargo/registry",
	".cargo/git",
}

// toolchainStateRoots are state directories a toolchain writes on every run
// and tolerates losing. Go's local telemetry counters live under the user
// config directory, outside every cache convention; without this root each
// confined go invocation reports a refusal.
func toolchainStateRoots() []string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return nil
	}
	return []string{filepath.Join(dir, "go", "telemetry")}
}
