package confine

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
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

// packageCacheDataRelRoots are package stores below Linux's data convention.
// Their surrounding tool homes also contain global installations and executables.
var packageCacheDataRelRoots = []string{
	"pnpm/store",
	"NuGet/http-cache",
	"NuGet/plugin-cache",
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

// standardCacheDataRoots returns conventional caches and bounded package stores.
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
	}
	if runtime.GOOS == "linux" {
		for _, path := range packageCacheDataRoots() {
			if packageCacheDirectoryAvailable(path) == nil {
				candidates = append(candidates, path)
			}
		}
	} else {
		candidates = append(candidates, os.Getenv("XDG_DATA_HOME"), join(home, ".local/share"),
			os.Getenv("XDG_STATE_HOME"), join(home, ".local/state"))
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

func packageCacheDataRoots() []string {
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		home, _ := os.UserHomeDir()
		if home == "" {
			return nil
		}
		data = filepath.Join(home, ".local/share")
	}
	roots := make([]string, 0, len(packageCacheDataRelRoots))
	for _, rel := range packageCacheDataRelRoots {
		roots = append(roots, filepath.Join(data, rel))
	}
	return roots
}

// Missing Linux roots cannot acquire a Landlock rule. Materialize only the
// declared stores; directory availability never widens the boundary's authority.
func preparePackageCacheDataRoots(c Confinement, mkdir func(fseffect.Location, os.FileMode) error) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	rules, err := filesystemRules(c)
	if err != nil {
		return err
	}
	for _, candidate := range packageCacheDataRoots() {
		if err := packageCacheDirectoryAvailable(candidate); err != nil {
			slog.Debug("package store directory unavailable", "path", candidate, "error", err)
			continue
		}
		path := fspath.CanonicalPath(candidate)
		if path == "" || !rules.Verdict(AccessWrite, path).Allowed {
			continue
		}
		err := mkdir(fseffect.Location{Root: string(filepath.Separator), Rel: strings.TrimPrefix(path, string(filepath.Separator))}, 0o700)
		if err != nil {
			// An optional cache's OS permissions must not prevent unrelated work.
			slog.Debug("package store directory unavailable", "path", path, "error", err)
		}
	}
	return nil
}

func packageCacheDirectoryAvailable(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return syscall.ENOTDIR
	}
	return nil
}
