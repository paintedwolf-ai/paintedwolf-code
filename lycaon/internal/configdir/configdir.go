// Package configdir resolves the host configuration root.
package configdir

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// DirNameProd is the XDG config segment for release builds.
	DirNameProd = "paintedwolf"
	// DirNameDev is the XDG config segment for development builds.
	DirNameDev = "paintedwolf-dev"

	// EnvConfigDir replaces the root on development and performance builds.
	EnvConfigDir = "LYCAON_CONFIG_DIR"
	// EnvDev selects the development leaf when EnvConfigDir is unset.
	EnvDev = "LYCAON_DEV"
	// EnvHarness enables authenticated test controls on the development channel.
	EnvHarness = "LYCAON_HARNESS"

	configDirMode = 0o700
)

// ChannelDirName returns the compiled channel's directory name.
func ChannelDirName() string {
	return channelDirNameForBuild(releaseBuild, os.Getenv(EnvDev))
}

// IsDevelopmentBuild reports whether the paintedwolf_release tag is absent.
func IsDevelopmentBuild() bool {
	return !releaseBuild
}

// IsPerformanceBuild reports whether performance seams are compiled.
func IsPerformanceBuild() bool {
	return performanceBuild
}

// IsDevelopmentChannel reports whether development storage is selected.
func IsDevelopmentChannel() bool {
	return IsDevelopmentBuild() && EnvTruthy(os.Getenv(EnvDev))
}

// IsHarnessChannel reports whether the isolated development harness is active.
func IsHarnessChannel() bool {
	return IsDevelopmentChannel() && EnvTruthy(os.Getenv(EnvHarness))
}

func channelDirNameForBuild(release bool, devValue string) string {
	if !release && EnvTruthy(devValue) {
		return DirNameDev
	}
	return DirNameProd
}

// Label returns a display path for the active configuration directory.
func Label() string {
	return "~/.config/" + ChannelDirName()
}

// LabelProd returns the release configuration label.
func LabelProd() string {
	return "~/.config/" + DirNameProd
}

// UserConfigDir returns the per-user config dir, creating it if missing.
func UserConfigDir() (string, error) {
	dir, err := configRoot()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, configDirMode); err != nil { // #nosec G703 -- Standard release builds ignore the override.
		return "", err
	}
	if err := restrictConfigDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func configRoot() (string, error) {
	if dir := configDirOverrideForBuild(releaseBuild, performanceBuild, os.Getenv(EnvConfigDir)); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return ChannelConfigRoot(home), nil
}

// ChannelConfigRoot is the active channel's root under a home directory.
func ChannelConfigRoot(home string) string {
	return filepath.Join(home, ".config", ChannelDirName())
}

func configDirOverrideForBuild(release, performance bool, value string) string {
	if release && !performance {
		return ""
	}
	return strings.TrimSpace(value)
}

// EnvTruthy parses supported enabled flag values.
func EnvTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
