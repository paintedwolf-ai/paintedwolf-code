package definition

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
)

type registryConfigFile struct {
	DefaultAmbient defaultAmbientYAML `yaml:"default_ambient_workflow"`
}

type defaultAmbientYAML struct {
	ID      string `yaml:"id"`
	Version string `yaml:"version"`
}

// LoadRegistryConfig reads catalog/workflows/registry.yaml.
func LoadRegistryConfig(workflowsDir extpacks.Source) (DefaultAmbientRef, error) {
	data, err := workflowsDir.Join(config.WorkflowRegistryFile).Read()
	if err != nil {
		return DefaultAmbientRef{}, fmt.Errorf("read workflow registry config: %w", err)
	}
	var cfg registryConfigFile
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return DefaultAmbientRef{}, fmt.Errorf("parse workflow registry config: %w", err)
	}
	id := strings.TrimSpace(cfg.DefaultAmbient.ID)
	ver := strings.TrimSpace(cfg.DefaultAmbient.Version)
	if id == "" || ver == "" {
		return DefaultAmbientRef{}, fmt.Errorf("default_ambient_workflow requires id and version")
	}
	return DefaultAmbientRef{ID: id, Version: ver}, nil
}
