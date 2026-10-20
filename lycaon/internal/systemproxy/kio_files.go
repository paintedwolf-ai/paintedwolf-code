package systemproxy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func kioConfigurationPath(lookupEnv func(string) (string, bool)) string {
	if dir, ok := lookupEnv("XDG_CONFIG_HOME"); ok && filepath.IsAbs(dir) {
		return filepath.Join(dir, "kioslaverc")
	}
	if home, ok := lookupEnv("HOME"); ok && filepath.IsAbs(home) {
		return filepath.Join(home, ".config", "kioslaverc")
	}
	return ""
}

// KDE reads the least specific file first. NoGlobals excludes kdeglobals,
// while kioslaverc still cascades through the XDG configuration directories.
func kioConfigurationPaths(lookupEnv func(string) (string, bool)) []string {
	dirs, _ := lookupEnv("XDG_CONFIG_DIRS")
	if dirs == "" {
		dirs = "/etc/xdg"
	}
	paths := []string{}
	for _, dir := range strings.Split(dirs, ":") {
		if filepath.IsAbs(dir) {
			path := filepath.Join(dir, "kioslaverc")
			if !slices.Contains(paths, path) {
				paths = append(paths, path)
			}
		}
	}
	slices.Reverse(paths)
	if path := kioConfigurationPath(lookupEnv); path != "" {
		paths = slices.DeleteFunc(paths, func(system string) bool { return system == path })
		paths = append(paths, path)
	}
	return paths
}

// Credentials for KDE proxies live in the wallet and are not read.
func readKIOConfiguration(lookupEnv func(string) (string, bool)) (Configuration, bool, error) {
	group := kioEntries{values: map[string]string{}, locked: map[string]bool{}}
	for _, path := range kioConfigurationPaths(lookupEnv) {
		raw, err := readKIOFile(path)
		if err != nil {
			return Configuration{}, false, fmt.Errorf("read KDE proxy configuration: %w", err)
		}
		immutable, err := group.merge(raw, lookupEnv)
		if err != nil {
			return Configuration{}, false, fmt.Errorf("read KDE proxy configuration: %w", err)
		}
		if immutable {
			break
		}
	}
	if !group.found {
		return Configuration{}, false, nil
	}
	config, err := parseKIOProxyGroup(group.values, lookupEnv)
	return config, err == nil, err
}

func readKIOFile(path string) (string, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxKIOBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxKIOBytes {
		return "", errors.New("KDE proxy configuration exceeds its size limit")
	}
	return string(raw), nil
}
