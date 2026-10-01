package configuration

import (
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/configdir"
)

// ModuleConfig holds device-specific scan settings.
type ModuleConfig struct {
	// SASTRulesPath labels the rules tree in scan evidence.
	SASTRulesPath string
	// UserConfigDir contains the device scan-hints overlay.
	UserConfigDir string
}

// DefaultModuleConfig builds the scan module settings for this device.
func DefaultModuleConfig() ModuleConfig {
	cfg := ModuleConfig{SASTRulesPath: config.ScannerRulesDir.String()}
	// Missing device configuration leaves bundled hints active.
	if dir, err := configdir.UserConfigDir(); err == nil {
		cfg.UserConfigDir = dir
	}
	return cfg
}
