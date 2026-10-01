package logview

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fseffect"
	"gopkg.in/yaml.v3"
)

// Config holds the persisted preferences for the logs viewer, shared by the CLI and
// the TUI. Flags override config; config overrides these defaults.
type Config struct {
	// Color is auto|always|never. auto enables color on a TTY.
	Color string `yaml:"color"`
	// UnescapeHTML decodes HTML entities (&#xA; &#34; …) in captured content so
	// worker digests read as plain text.
	UnescapeHTML bool `yaml:"unescape_html"`
	// DefaultView is the stream the TUI opens on: llm|http|sse|sessions.
	DefaultView string `yaml:"default_view"`
	// TimeFormat is clock|iso for row and header timestamps.
	TimeFormat string `yaml:"time_format"`
	// Follow makes the TUI tail the live capture on launch.
	Follow bool `yaml:"follow"`
	// HideSystem hides the system message in prompt views by default.
	HideSystem bool `yaml:"hide_system"`
	// HeadLines caps content lines per message in prompt views (0 = unlimited).
	HeadLines int `yaml:"head_lines"`
}

// DefaultConfig is the built-in baseline used when no config file exists.
func DefaultConfig() Config {
	return Config{
		Color:        "auto",
		UnescapeHTML: false,
		DefaultView:  "llm",
		TimeFormat:   "clock",
		Follow:       false,
		HideSystem:   false,
		HeadLines:    0,
	}
}

// ConfigPath is the user config file location (~/.config/paintedwolf/logview.yaml).
func ConfigPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logview.yaml"), nil
}

// LoadConfig reads the config file, falling back to defaults for a missing file and
// merging any present keys over the defaults.
func LoadConfig() (Config, error) {
	cfg := DefaultConfig()
	path, err := ConfigPath()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- fixed path under the user config dir
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, cfg.validate()
}

// Save writes the config to its user path with 0600 perms.
func (c Config) Save() error {
	if err := c.validate(); err != nil {
		return err
	}
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(data),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}

func (c Config) validate() error {
	if c.Color != "" && c.Color != "auto" && c.Color != "always" && c.Color != "never" {
		return fmt.Errorf("color must be auto|always|never, got %q", c.Color)
	}
	if c.TimeFormat != "" && c.TimeFormat != "clock" && c.TimeFormat != "iso" {
		return fmt.Errorf("time_format must be clock|iso, got %q", c.TimeFormat)
	}
	return nil
}

// configFields maps setting keys to typed get/set accessors for `logs config`.
var configFields = map[string]struct {
	get func(Config) string
	set func(*Config, string) error
}{
	"color": {
		get: func(c Config) string { return c.Color },
		set: func(c *Config, v string) error { c.Color = v; return nil },
	},
	"unescape_html": {
		get: func(c Config) string { return strconv.FormatBool(c.UnescapeHTML) },
		set: func(c *Config, v string) error { return setBool(&c.UnescapeHTML, v) },
	},
	"default_view": {
		get: func(c Config) string { return c.DefaultView },
		set: func(c *Config, v string) error { c.DefaultView = v; return nil },
	},
	"time_format": {
		get: func(c Config) string { return c.TimeFormat },
		set: func(c *Config, v string) error { c.TimeFormat = v; return nil },
	},
	"follow": {
		get: func(c Config) string { return strconv.FormatBool(c.Follow) },
		set: func(c *Config, v string) error { return setBool(&c.Follow, v) },
	},
	"hide_system": {
		get: func(c Config) string { return strconv.FormatBool(c.HideSystem) },
		set: func(c *Config, v string) error { return setBool(&c.HideSystem, v) },
	},
	"head_lines": {
		get: func(c Config) string { return strconv.Itoa(c.HeadLines) },
		set: func(c *Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("head_lines must be a number, got %q", v)
			}
			c.HeadLines = n
			return nil
		},
	},
}

func setBool(dst *bool, v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("expected true|false, got %q", v)
	}
	*dst = b
	return nil
}

// ConfigKeys lists the settable keys in stable order.
func ConfigKeys() []string {
	keys := make([]string, 0, len(configFields))
	for k := range configFields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// GetField returns the string value of a single setting.
func (c Config) GetField(key string) (string, error) {
	f, ok := configFields[key]
	if !ok {
		return "", fmt.Errorf("unknown setting %q (try: %s)", key, strings.Join(ConfigKeys(), ", "))
	}
	return f.get(c), nil
}

// SetField updates a single setting in place after validating the value.
func (c *Config) SetField(key, value string) error {
	f, ok := configFields[key]
	if !ok {
		return fmt.Errorf("unknown setting %q (try: %s)", key, strings.Join(ConfigKeys(), ", "))
	}
	if err := f.set(c, value); err != nil {
		return err
	}
	return c.validate()
}
