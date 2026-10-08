package confine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// standardCacheDataRoots returns conventional cache and data directories.
func standardCacheDataRoots() []string {
	roots := []string{}
	home, _ := os.UserHomeDir()
	join := func(base, rel string) string {
		if base == "" {
			return ""
		}
		return filepath.Join(base, rel)
	}
	candidates := []string{
		os.Getenv("XDG_CACHE_HOME"), join(home, ".cache"),
		os.Getenv("XDG_DATA_HOME"), join(home, ".local/share"),
		os.Getenv("XDG_STATE_HOME"), join(home, ".local/state"),
	}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, join(home, "Library/Caches"))
	}
	for _, rel := range packageCacheHomeRelRoots {
		candidates = append(candidates, join(home, rel))
	}
	candidates = append(candidates, toolchainStateRoots()...)
	candidates = append(candidates, environmentWriteRoots()...)
	for _, c := range candidates {
		if strings.TrimSpace(c) != "" {
			roots = append(roots, c)
		}
	}
	return roots
}
