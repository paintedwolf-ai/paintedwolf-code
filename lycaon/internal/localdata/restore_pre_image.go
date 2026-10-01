package localdata

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// Restore transaction directories live directly under the config root, one pair
// per transaction, named with the transaction id.
const (
	// RestoreStagingDirPrefix holds the archive bytes awaiting apply.
	RestoreStagingDirPrefix = ".restore-staging"
	// RestorePreImageDirPrefix holds the live state a restore displaced. For a
	// full restore that is every durable file and history directory.
	RestorePreImageDirPrefix = ".restore-recovery"
)

// RestorePreImage is one retained pre-image directory.
type RestorePreImage struct {
	Path    string
	Name    string
	Bytes   int64
	ModTime time.Time
}

// RestorePreImages lists retained pre-images under base, newest first.
func RestorePreImages(base string) ([]RestorePreImage, error) {
	base = filepath.Clean(strings.TrimSpace(base))
	if base == "" || base == "." {
		return nil, nil
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config root: %w", err)
	}
	var out []RestorePreImage
	for _, entry := range entries {
		if !entry.IsDir() || !isRestorePreImageName(entry.Name()) {
			continue
		}
		path := filepath.Join(base, entry.Name())
		size, sizeErr := dirOrFileBytes(path)
		if sizeErr != nil {
			// An unreadable pre-image still lists so pruning can reach it.
			size = 0
		}
		image := RestorePreImage{Path: path, Name: entry.Name(), Bytes: size}
		if info, infoErr := entry.Info(); infoErr == nil {
			image.ModTime = info.ModTime()
		}
		out = append(out, image)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ModTime.Equal(out[j].ModTime) {
			return out[i].Name > out[j].Name
		}
		return out[i].ModTime.After(out[j].ModTime)
	})
	return out, nil
}

// RestorePreImageBytes reports the retained pre-image total under base.
func RestorePreImageBytes(base string) (int64, error) {
	images, err := RestorePreImages(base)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, image := range images {
		total += image.Bytes
	}
	return total, nil
}

// PruneRestorePreImages removes pre-images except those identified by keepPaths.
func PruneRestorePreImages(base string, keepPaths ...string) ([]RestorePreImage, error) {
	images, err := RestorePreImages(base)
	if err != nil {
		return nil, err
	}
	keep := make([]os.FileInfo, 0, len(keepPaths))
	for _, path := range keepPaths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect retained restore pre-image: %w", err)
		}
		keep = append(keep, info)
	}
	var removed []RestorePreImage
	var errs []string
	for _, image := range images {
		info, err := os.Lstat(image.Path)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", image.Name, err))
			continue
		}
		// Directory identity is stable across configuration path aliases.
		if slices.ContainsFunc(keep, func(retained os.FileInfo) bool { return os.SameFile(info, retained) }) {
			continue
		}
		if err := os.RemoveAll(image.Path); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", image.Name, err))
			continue
		}
		removed = append(removed, image)
	}
	if len(errs) > 0 {
		return removed, fmt.Errorf("remove restore pre-image: %s", strings.Join(errs, "; "))
	}
	return removed, nil
}

func isRestorePreImageName(name string) bool {
	return strings.HasPrefix(name, RestorePreImageDirPrefix+"-")
}
