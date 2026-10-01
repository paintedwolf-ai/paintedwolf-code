package catalog

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"gopkg.in/yaml.v3"
)

// ProjectScannersRelPath is the project-tier scanner overlay relative path.
func ProjectScannersRelPath() string { return settingsoverlay.Rel("scanners.yaml") }

// ProjectScannersPath returns {projectDir}/<overlay>/scanners.yaml.
func ProjectScannersPath(projectDir string) string {
	return filepath.Join(projectDir, ProjectScannersRelPath())
}

func loadOptionalScannerConfig(path string) (*ScannerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var cfg ScannerConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}
