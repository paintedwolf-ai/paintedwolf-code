package toolstest

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
)

// ToolsConfig is the catalog tool allowlist.
type ToolsConfig struct {
	Tools []string `yaml:"tools"`
}

// LoadToolsConfig reads the bundled allowlist.
func LoadToolsConfig() (*ToolsConfig, error) {
	data, err := config.Read(config.LycaonTools)
	if err != nil {
		return nil, err
	}
	var cfg ToolsConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ParseCatalogToolIDs returns the allowlisted catalog tool ids.
func ParseCatalogToolIDs() ([]string, error) {
	cfg, err := LoadToolsConfig()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(cfg.Tools))
	for _, id := range cfg.Tools {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// ValidateCatalogAllowlistSync fails when an allowlisted id is not registered.
func ValidateCatalogAllowlistSync(registered map[string]bool) error {
	ids, err := ParseCatalogToolIDs()
	if err != nil {
		return err
	}
	var missing []string
	for _, id := range ids {
		if !registered[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("allowlisted tools not registered: %s", strings.Join(missing, ", "))
	}
	return nil
}
