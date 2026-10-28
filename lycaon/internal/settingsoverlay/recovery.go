package settingsoverlay

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	RecoveryDirName    = "recovery"
	RecoveryRelPath    = ".paintedwolf/" + RecoveryDirName
	RecoveryDirRelPath = RecoveryRelPath
)

// RecoveryDir returns the host-owned move-aside directory under rootPath.
func RecoveryDir(rootPath string) string {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(rootPath), DirName(), RecoveryDirName)
}

// IsRecoveryRelPath reports whether a root-relative path names the recovery
// directory or an entry inside it.
func IsRecoveryRelPath(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel))), "./")
	return rel == RecoveryRelPath || strings.HasPrefix(rel, RecoveryRelPath+"/")
}

// HasRecoverySegment reports whether path contains the .paintedwolf/recovery segment.
func HasRecoverySegment(path string) bool {
	slash := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	return slash == RecoveryRelPath ||
		strings.HasPrefix(slash, RecoveryRelPath+"/") ||
		strings.HasSuffix(slash, "/"+RecoveryRelPath) ||
		strings.Contains(slash, "/"+RecoveryRelPath+"/")
}

// IsRecoveryOnlyDir reports whether dir is a .paintedwolf directory whose
// only entry on disk is recovery.
func IsRecoveryOnlyDir(dir string) bool {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if filepath.Base(dir) != DirName() {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		return false
	}
	return entries[0].Name() == RecoveryDirName
}
