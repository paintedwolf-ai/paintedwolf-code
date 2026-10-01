package settings

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

const settingsFileMode = 0o600

func userApprovalsPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsoverlay.BasenameApprovals), nil
}

func userLimitsPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsoverlay.BasenameLimits), nil
}

func userFileSummariesPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "file-summaries.yaml"), nil
}

func userPowerPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "power.yaml"), nil
}

func projectApprovalsPath(projectDir string) string {
	return settingsoverlay.ProjectOverlayPath(projectDir, settingsoverlay.BasenameApprovals)
}

func projectLimitsPath(projectDir string) string {
	return settingsoverlay.ProjectOverlayPath(projectDir, settingsoverlay.BasenameLimits)
}

// writeSettingsFile atomically replaces a private settings file.
func writeSettingsFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(data),
		Mode:     settingsFileMode,
		DirMode:  0o700,
	})
	return err
}
