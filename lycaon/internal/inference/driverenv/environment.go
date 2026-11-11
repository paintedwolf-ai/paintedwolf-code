// Package driverenv prepares graphics drivers for isolated inference processes.
package driverenv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fseffect"
)

const resourceName = "vulkan-drivers.json"

type configuration struct {
	FormatVersion int      `json:"format_version"`
	MesaICDs      []string `json:"mesa_icds"`
}

func readConfiguration(root string) (result *configuration, err error) {
	if root == "" {
		return nil, nil
	}
	file, err := os.Open(filepath.Join(root, "generation", resourceName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return nil, err
	}
	if len(data) > 65536 {
		return nil, fmt.Errorf("driver configuration exceeds its bound")
	}
	var config configuration
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF || config.FormatVersion != 1 || len(config.MesaICDs) == 0 || len(config.MesaICDs) > 32 {
		return nil, fmt.Errorf("invalid Vulkan driver configuration")
	}
	seen := map[string]bool{}
	for _, path := range config.MesaICDs {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, ":\x00\n\r") || seen[path] {
			return nil, fmt.Errorf("invalid Vulkan ICD path")
		}
		seen[path] = true
	}
	return &config, nil
}

func environment(config configuration, libraries []string, apiVersion string) ([]string, func(), error) {
	noop := func() {}
	drivers := strings.Join(config.MesaICDs, ":")
	if len(libraries) == 0 {
		return []string{"VK_DRIVER_FILES=" + drivers}, noop, nil
	}
	directory, err := os.MkdirTemp("", "paintedwolf-vulkan-")
	if err != nil {
		return nil, noop, err
	}
	var once sync.Once
	cleanup := func() { once.Do(func() { _ = os.RemoveAll(directory) }) }
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	payload, err := json.Marshal(struct {
		Format string            `json:"file_format_version"`
		ICD    map[string]string `json:"ICD"`
	}{Format: "1.0.0", ICD: map[string]string{"library_path": libraries[len(libraries)-1], "api_version": apiVersion}})
	if err != nil {
		cleanup()
		return nil, noop, err
	}

	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: resolved, Rel: "nvidia.json"}, Source: bytes.NewReader(payload), Mode: 0o600,
	})
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	// The ICD must enter through the Vulkan loader without interposing its symbols.
	return []string{"VK_DRIVER_FILES=" + drivers + ":" + filepath.Join(resolved, "nvidia.json"),
		"LD_PRELOAD=" + strings.Join(libraries[:len(libraries)-1], ":")}, cleanup, nil
}
