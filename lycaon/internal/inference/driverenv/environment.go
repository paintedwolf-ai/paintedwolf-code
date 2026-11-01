// Package driverenv prepares graphics drivers for isolated inference processes.
package driverenv

import (
	"bytes"
	"encoding/json"
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

func readConfiguration(root string) (*configuration, error) {
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
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return nil, err
	}
	if len(data) > 65536 {
		return nil, fmt.Errorf("Vulkan driver configuration exceeds its bound")
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

func environment(config configuration, libraries []string) ([]string, func(), error) {
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
	payload, _ := json.Marshal(map[string]any{
		"file_format_version": "1.0.0",
		"ICD":                 map[string]string{"library_path": libraries[len(libraries)-1], "api_version": "1.3.0"},
	})
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: resolved, Rel: "nvidia.json"}, Source: bytes.NewReader(payload), Mode: 0o600,
	})
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	return []string{"VK_DRIVER_FILES=" + drivers + ":" + filepath.Join(resolved, "nvidia.json"),
		"LD_PRELOAD=" + strings.Join(libraries, ":")}, cleanup, nil
}
