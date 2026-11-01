//go:build linux

package driverenv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func nvidiaAPIVersion(root string, uid uint32) (string, error) {
	for _, directory := range []string{"etc/vulkan/icd.d", "usr/share/vulkan/icd.d"} {
		for _, name := range []string{"nvidia_icd.json", "nvidia_icd.x86_64.json", "nvidia_icd.aarch64.json"} {
			path, err := filepath.EvalSymlinks(filepath.Join(root, directory, name))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return "", err
			}
			if err := systemOwnedFile(path, root, uid); err != nil {
				return "", err
			}
			file, err := os.Open(path)
			if err != nil {
				return "", err
			}
			raw, readErr := io.ReadAll(io.LimitReader(file, 65537))
			file.Close()
			if readErr != nil {
				return "", readErr
			}
			return manifestAPIVersion(raw)
		}
	}
	return "", fmt.Errorf("NVIDIA Vulkan driver manifest is missing")
}

func manifestAPIVersion(raw []byte) (string, error) {
	if len(raw) > 65536 {
		return "", fmt.Errorf("NVIDIA Vulkan manifest exceeds its bound")
	}
	var manifest struct {
		Format string `json:"file_format_version"`
		ICD    struct {
			API string `json:"api_version"`
		} `json:"ICD"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&manifest); err != nil {
		return "", err
	}
	if decoder.Decode(new(any)) != io.EOF || (manifest.Format != "1.0.0" && manifest.Format != "1.0.1") || !validAPIVersion(manifest.ICD.API) {
		return "", fmt.Errorf("invalid NVIDIA Vulkan driver manifest")
	}
	return manifest.ICD.API, nil
}

func validAPIVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for index, bits := range []int{7, 10, 12} {
		value, err := strconv.ParseUint(parts[index], 10, bits)
		if err != nil || (index == 0 && value == 0) {
			return false
		}
	}
	return true
}
