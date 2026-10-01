package extpacks

import (
	"path/filepath"
	"strings"
)

// AcquireIntentLocks serializes device and project intent reads.
func AcquireIntentLocks(projectDirs []string) (func(), error) {
	devicePath, err := DeviceDesiredPath()
	if err != nil {
		return nil, err
	}
	paths := []string{devicePath}
	seen := map[string]struct{}{filepath.Clean(devicePath): {}}
	for _, projectDir := range projectDirs {
		if strings.TrimSpace(projectDir) == "" {
			continue
		}
		path := ProjectDesiredPath(projectDir)
		key := filepath.Clean(path)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		paths = append(paths, path)
	}
	releases := make([]func(), 0, len(paths))
	for _, path := range paths {
		unlock := lockExtensionPath(path)
		releaseFile, lockErr := lockExtensionPathAcrossProcesses(path)
		if lockErr != nil {
			unlock()
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			return nil, lockErr
		}
		releases = append(releases, func() {
			releaseFile()
			unlock()
		})
	}
	return func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}, nil
}
