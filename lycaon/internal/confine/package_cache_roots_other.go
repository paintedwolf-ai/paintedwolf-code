//go:build (!linux && !darwin && !windows) || android || ios

package confine

import (
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
)

func platformCacheDataRoots(home string) []string {
	return []string{
		os.Getenv("XDG_CACHE_HOME"), packageCacheHomePath(home, ".cache"),
		os.Getenv("XDG_DATA_HOME"), packageCacheHomePath(home, ".local/share"),
		os.Getenv("XDG_STATE_HOME"), packageCacheHomePath(home, ".local/state"),
	}
}

func preparePackageCacheDataRoots(Confinement, func(fseffect.Location, os.FileMode) (int, error)) error {
	return nil
}
