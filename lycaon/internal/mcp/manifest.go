package mcp

import (
	"bytes"

	"github.com/lycaon/lycaon/config"
	"gopkg.in/yaml.v3"
)

// DefaultDistroMCPConfigPath is the bundled MCP catalog relative to the lycaon module root.

// DistroMCPConfig is the on-disk distro-mcp.yaml shape.
type DistroMCPConfig struct {
	Providers []MCPProviderEntry       `yaml:"providers"`
	Profiles  map[string]MCPProfileRef `yaml:"profiles"`
}

// MCPProfileRef binds profile names to provider IDs.
type MCPProfileRef struct {
	Providers []string `yaml:"providers"`
}

// LoadDistroMCPConfig reads the MCP distro manifest from path.
func LoadDistroMCPConfig() (*DistroMCPConfig, error) {
	data, err := config.Read(config.DistroMCP)
	if err != nil {
		return nil, err
	}
	var cfg DistroMCPConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if err := duplicateDistroIDs(cfg.Providers); err != nil {
		return nil, err
	}
	return &cfg, nil
}
