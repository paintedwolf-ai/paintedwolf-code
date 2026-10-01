// Package settingsoverlay defines the project overlay layout.
package settingsoverlay

import (
	"path/filepath"
	"strings"
)

// DirName is shared by all build channels.
func DirName() string {
	return ".paintedwolf"
}

// Rel returns a slash-separated path under the overlay directory.
func Rel(segments ...string) string {
	out := DirName()
	for _, seg := range segments {
		seg = strings.Trim(strings.TrimSpace(seg), "/")
		if seg == "" {
			continue
		}
		out += "/" + seg
	}
	return out
}

// Dir returns the overlay directory inside root, as an on-disk path.
func Dir(root string) string {
	return filepath.Join(root, DirName())
}

// Settings files at the overlay root.
const (
	BasenameIgnores          = "ignores.yaml"
	BasenameApprovals        = "approvals.yaml"
	BasenameLimits           = "limits.yaml"
	BasenameMCP              = "mcp.yaml"
	BasenameVerify           = "verify.yaml"
	BasenameReview           = "review.yaml"
	BasenameModelPolicy      = "model-policy.yaml"
	BasenameStandingPatterns = "standing-patterns.yaml"
	BasenameHostResources    = "host-resources.yaml"
	BasenameExtensions       = "extensions.yaml"
	BasenameExtensionsLock   = "extensions.lock.yaml"
)

// SettingsOverlayBasenames returns the settings files at the overlay root.
func SettingsOverlayBasenames() []string {
	return []string{
		BasenameApprovals,
		BasenameLimits,
		BasenameMCP,
		BasenameVerify,
		BasenameReview,
		BasenameModelPolicy,
		BasenameStandingPatterns,
		BasenameHostResources,
		BasenameExtensions,
		BasenameExtensionsLock,
	}
}

// Configuration read from the overlay beside its settings files.
const (
	BasenamePostures = "postures.yaml"
	RulesDirName     = "rules"
	WorkflowsDirName = "workflows"
)

// ScanConfigBasenames are the project scanner configuration files.
func ScanConfigBasenames() []string {
	return []string{BasenameIgnores, "scanners.yaml", "scan-hints.yaml", "detection-packs.yaml", "source-scope.yaml"}
}

// ProjectOverlayPath places a basename under the project overlay.
func ProjectOverlayPath(projectDir, basename string) string {
	return filepath.Join(Dir(projectDir), filepath.Base(basename))
}
