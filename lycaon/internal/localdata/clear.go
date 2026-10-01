package localdata

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/enginepaths"
)

// clearTree removes paths inside base and rejects symlinks escaping it.
func clearTree(base, target string) error {
	base = filepath.Clean(base)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("refusing clear outside config dir: %s", target)
	}
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(target)
		if err != nil {
			// Dangling or unreadable links are removed without following them.
			return os.Remove(target)
		}
		rel2, err := filepath.Rel(base, filepath.Clean(resolved))
		if err != nil || rel2 == ".." || strings.HasPrefix(rel2, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("refusing symlink escape: %s → %s", target, resolved)
		}
	}
	return os.RemoveAll(target)
}

// clearDirContents replaces dir with an empty directory.
func clearDirContents(base, dir string) error {
	if err := clearTree(base, dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}

func clearDebugLogs(base string) error {
	return clearTree(base, debugpaths.DebugRootUnder(base))
}

func clearSourceObservations(base string) error {
	for _, path := range sourceObservationPaths(base) {
		if err := clearTree(base, path); err != nil {
			return err
		}
	}
	return nil
}

func clearExtensionCache(base string) error {
	if err := clearDirContents(base, enginepaths.ExtensionsCacheRootUnder(base)); err != nil {
		return err
	}
	return clearDirContents(base, enginepaths.ExtensionsMetaRootUnder(base))
}

func clearScanScratch(base string) error {
	return clearTree(base, enginepaths.VerifyDetectPathUnder(base))
}
