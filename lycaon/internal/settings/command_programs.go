package settings

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/configdir"
)

// CommandExternalOffered and CommandExternalHidden are the external selector
// settings: offered keeps `external -- NAME` in the command schema and
// guidance; hidden removes it there and refuses it with a typed code.
const (
	CommandExternalOffered = "offered"
	CommandExternalHidden  = "hidden"
)

// CommandProgramsSettings is the device overlay at
// ~/.config/paintedwolf/command-programs.yaml. Absent keys keep defaults.
type CommandProgramsSettings struct {
	ExternalSelector string `yaml:"external_selector,omitempty"`
}

// ExternalHidden reports whether the explicit external selector is withdrawn.
func (s CommandProgramsSettings) ExternalHidden() bool {
	return s.ExternalSelector == CommandExternalHidden
}

// LoadCommandProgramsSettings reads the device overlay; a missing file is the
// default, and an unknown selector value fails startup.
func LoadCommandProgramsSettings() (CommandProgramsSettings, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return CommandProgramsSettings{}, err
	}
	return LoadCommandProgramsSettingsAt(filepath.Join(dir, "command-programs.yaml"))
}

// LoadCommandProgramsSettingsAt reads the overlay at path.
func LoadCommandProgramsSettingsAt(path string) (CommandProgramsSettings, error) {
	var out CommandProgramsSettings
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return CommandProgramsSettings{ExternalSelector: CommandExternalOffered}, nil
	}
	if err != nil {
		return out, err
	}
	if err := config.DecodeYAML(data, &out); err != nil {
		return out, fmt.Errorf("parse command programs settings: %w", err)
	}
	switch out.ExternalSelector {
	case "":
		out.ExternalSelector = CommandExternalOffered
	case CommandExternalOffered, CommandExternalHidden:
	default:
		return out, fmt.Errorf("command programs external_selector %q is not offered or hidden", out.ExternalSelector)
	}
	return out, nil
}
