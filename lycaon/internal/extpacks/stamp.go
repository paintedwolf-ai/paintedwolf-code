package extpacks

import (
	"fmt"
	"os"
	"strings"
)

// DesiredStamp identifies all intent files used by a resolve.
func DesiredStamp(projectDirs []string) string {
	deviceDesired := ""
	deviceLock := ""
	if path, err := DeviceDesiredPath(); err == nil {
		deviceDesired = path
	}
	if path, err := DeviceLockPath(); err == nil {
		deviceLock = path
	}
	stamps := []string{fileStamp(deviceDesired), fileStamp(deviceLock)}
	seen := map[string]struct{}{}
	for _, projectDir := range projectDirs {
		if dir := strings.TrimSpace(projectDir); dir != "" {
			if _, exists := seen[dir]; exists {
				continue
			}
			seen[dir] = struct{}{}
			stamps = append(stamps, fileStamp(ProjectDesiredPath(dir)))
		}
	}
	return strings.Join(stamps, "|")
}

// MetaPackCacheStamp identifies the set of installed meta-packs, which cache
// mutations change without touching desired state.
func MetaPackCacheStamp() string {
	root, err := MetaPackCacheRoot()
	if err != nil {
		return "-"
	}
	return fileStamp(root)
}

// DeviceDesiredStamp identifies device intent and lock state.
func DeviceDesiredStamp() string {
	return DesiredStamp(nil)
}

func fileStamp(path string) string {
	if strings.TrimSpace(path) == "" {
		return "-"
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "-"
	}
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
}
