package sandbox

import (
	"github.com/lycaon/lycaon/config"
)

// Config is loaded from sandbox.yaml.
type Config struct {
	ProjectRootRequired bool `yaml:"project_root_required"`
	RejectSymlinkEscape bool `yaml:"reject_symlink_escape"`
}

// LoadConfig reads the bundled sandbox boundary rules.
func LoadConfig() (Config, error) {
	data, err := config.Read(config.SandboxProfile)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
