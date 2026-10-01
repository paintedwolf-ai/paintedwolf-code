package tools

import (
	"github.com/lycaon/lycaon/config"
)

// ToolsConfig is the catalog tool allowlist.
type ToolsConfig struct {
	Tools []string `yaml:"tools"`
}

// LoadToolsConfig reads the shipped tool allowlists. It decodes only; the YAML
// shape is checked by the strict-decode config contract.
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
