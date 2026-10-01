package sandbox

import (
	"strings"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

const vcsDirName = ".git"

// IsHiddenName reports whether a name is dot-prefixed.
func IsHiddenName(name string) bool {
	return strings.HasPrefix(name, ".")
}

// IsVCSDirBaseName reports whether baseName is VCS metadata (.git).
func IsVCSDirBaseName(baseName string) bool {
	return baseName == vcsDirName
}

// IsEngineOverlayBaseName reports whether baseName is the project settings overlay.
func IsEngineOverlayBaseName(baseName string) bool {
	return baseName == settingsoverlay.DirName()
}

// ShouldSkipDirBaseName reports whether baseName is engine overlay or VCS metadata.
func ShouldSkipDirBaseName(baseName string) bool {
	return IsVCSDirBaseName(baseName) || IsEngineOverlayBaseName(baseName)
}

// ShouldSkipDir reports whether a directory lies under engine overlay or VCS metadata.
func ShouldSkipDir(relSlash, baseName string) bool {
	if ShouldSkipDirBaseName(baseName) {
		return true
	}
	return pathHasDirSegment(relSlash, vcsDirName) ||
		pathHasDirSegment(relSlash, settingsoverlay.DirName())
}

func pathHasDirSegment(relSlash, dirName string) bool {
	return relSlash == dirName ||
		strings.HasPrefix(relSlash, dirName+"/") ||
		strings.Contains(relSlash, "/"+dirName+"/")
}
