package localdata

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/enginepaths"
)

// dirOrFileBytes returns best-effort byte size for a file or directory tree.
// Missing paths return 0, nil.
func dirOrFileBytes(p string) (int64, error) {
	info, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var total int64
	err = filepath.WalkDir(p, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // Unreadable entries do not fail size reporting.
		}
		total += fi.Size()
		return nil
	})
	return total, err
}

func debugLogsBytes(base string) (int64, error) {
	return dirOrFileBytes(debugpaths.DebugRootUnder(base))
}

func sourceObservationsBytes(base string) (int64, error) {
	var total int64
	for _, path := range sourceObservationPaths(base) {
		n, err := dirOrFileBytes(path)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func extensionCacheBytes(base string) (int64, error) {
	a, err := dirOrFileBytes(enginepaths.ExtensionsCacheRootUnder(base))
	if err != nil {
		return 0, err
	}
	b, err := dirOrFileBytes(enginepaths.ExtensionsMetaRootUnder(base))
	if err != nil {
		return a, err
	}
	return a + b, nil
}

func scanScratchBytes(base string) (int64, error) {
	return dirOrFileBytes(enginepaths.VerifyDetectPathUnder(base))
}
