package llm

import (
	"path/filepath"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

func userProvidersLocalPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "providers.local.yaml"), nil
}

func userModelPolicyPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsoverlay.BasenameModelPolicy), nil
}

// privateConfigFileMode protects user configuration files.
const privateConfigFileMode = 0o600

// privateConfigDirMode protects user configuration directories.
const privateConfigDirMode = 0o700
