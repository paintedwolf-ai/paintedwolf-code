package secretmint

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
)

// Catalog combines explicit credential keys with name-based recognition.
type Catalog struct {
	Surfaces      map[string]Surface `yaml:"surfaces"`
	ExcludedKeys  []string           `yaml:"excluded_keys"`
	CommandImages []CommandImage     `yaml:"command_images"`
	ValueFlags    []string           `yaml:"value_flags"`
	EnvKeys       []string           `yaml:"env_keys"`
	FileKeys      []string           `yaml:"file_keys"`
	KeyTerms      []string           `yaml:"key_terms"`
	KeyTermPairs  [][2]string        `yaml:"key_term_pairs"`
	SQLMint       []SQLMint          `yaml:"sql_mint"`
}

// Surface selects a parser for a declared execution or authoring payload.
type Surface struct {
	Kind       string `yaml:"kind"`
	ContentArg string `yaml:"content_arg"`
}

// CommandImage identifies program-specific positional or short-flag syntax.
type CommandImage struct {
	Image        string   `yaml:"image"`
	RequireFlags []string `yaml:"require_flags"`
	Value        string   `yaml:"value"`
	ValueFlags   []string `yaml:"value_flags"`
}

const valueLastPositional = "last_positional"

// SQLMint identifies a password-setting SQL fragment.
type SQLMint struct {
	Pattern string `yaml:"pattern"`
}

type defaultsFile struct {
	Passwords []string `yaml:"passwords"`
}

func loadCatalog() (Catalog, error) {
	raw, err := config.Read(config.SecretMintAssignments)
	if err != nil {
		return Catalog{}, fmt.Errorf("mint assignments: %w", err)
	}
	var cat Catalog
	if err := config.DecodeYAML(raw, &cat); err != nil {
		return Catalog{}, fmt.Errorf("mint assignments: %w", err)
	}
	if err := cat.validate(); err != nil {
		return Catalog{}, err
	}
	return cat, nil
}

func loadDefaultPasswords() ([]string, error) {
	raw, err := config.Read(config.SecretMintDefaults)
	if err != nil {
		return nil, fmt.Errorf("secret-mint defaults: %w", err)
	}
	var file defaultsFile
	if err := config.DecodeYAML(raw, &file); err != nil {
		return nil, fmt.Errorf("secret-mint defaults: %w", err)
	}
	return file.Passwords, nil
}

func loadVendoredPasswords() ([]string, error) {
	raw, err := config.Read(config.SecretMintVendorDir.Join("zxcvbn-passwords", "passwords.txt"))
	if err != nil {
		return nil, fmt.Errorf("zxcvbn passwords: %w", err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

func (c Catalog) validate() error {
	for tool, surface := range c.Surfaces {
		if strings.TrimSpace(tool) == "" {
			return fmt.Errorf("credential catalog: empty tool name")
		}
		switch surface.Kind {
		case "command":
			if surface.ContentArg != "" {
				return fmt.Errorf("credential catalog: command surface %s has a content argument", tool)
			}
		case "terminal", "content":
			if surface.ContentArg == "" {
				return fmt.Errorf("credential catalog: surface %s needs a content argument", tool)
			}
		default:
			return fmt.Errorf("credential catalog: unknown surface kind %q", surface.Kind)
		}
	}
	for _, image := range c.CommandImages {
		if image.Image == "" || (image.Value != "" && image.Value != valueLastPositional) {
			return fmt.Errorf("credential catalog: invalid command image %q", image.Image)
		}
	}
	return nil
}
