//go:build linux && !android

package confine

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

func platformCacheDataRoots(home string) []string {
	roots := []string{os.Getenv("XDG_CACHE_HOME"), packageCacheHomePath(home, ".cache")}
	for _, path := range packageCacheDataRoots() {
		if packageCacheDirectoryAvailable(path) == nil {
			roots = append(roots, path)
		}
	}
	return roots
}

// packageCacheDataRelRoots are package stores below Linux's data convention.
// Their surrounding tool homes also contain global installations and executables.
var packageCacheDataRelRoots = []string{
	"pnpm/store",
	"NuGet/http-cache",
	"NuGet/plugin-cache",
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
func preparePackageCacheDataRoots(c Confinement, mkdir func(fseffect.Location, os.FileMode) (int, error)) error {
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
		_, err := mkdir(fseffect.Location{Root: string(filepath.Separator), Rel: strings.TrimPrefix(path, string(filepath.Separator))}, 0o700)
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
