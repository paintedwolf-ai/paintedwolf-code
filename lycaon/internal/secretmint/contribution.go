package secretmint

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/config"
)

// Contribution adds recognition without changing host parsers or exclusions.
type Contribution struct {
	Version       int                `yaml:"version"`
	Surfaces      map[string]Surface `yaml:"surfaces"`
	CommandImages []CommandImage     `yaml:"command_images"`
	ValueFlags    []string           `yaml:"value_flags"`
	EnvKeys       []string           `yaml:"env_keys"`
	FileKeys      []string           `yaml:"file_keys"`
	KeyTerms      []string           `yaml:"key_terms"`
	KeyTermPairs  [][2]string        `yaml:"key_term_pairs"`
}

const maxContributionBytes = 64 * 1024
const maxContributionEntries = 256

// ParseContribution accepts one bounded, closed YAML document.
func ParseContribution(body []byte) (Contribution, error) {
	var c Contribution
	if len(body) > maxContributionBytes {
		return c, fmt.Errorf("credential slots: document exceeds %d bytes", maxContributionBytes)
	}
	if err := config.DecodeYAML(body, &c); err != nil {
		return c, fmt.Errorf("credential slots: %w", err)
	}
	return c, c.validate()
}

func (c Contribution) validate() error {
	if c.Version != 1 {
		return fmt.Errorf("credential slots: version must be 1")
	}
	count := len(c.Surfaces) + len(c.CommandImages) + len(c.ValueFlags) + len(c.EnvKeys) + len(c.FileKeys) + len(c.KeyTerms) + len(c.KeyTermPairs)
	if count == 0 || count > maxContributionEntries {
		return fmt.Errorf("credential slots: require 1–%d entries", maxContributionEntries)
	}
	if err := (Catalog{Surfaces: c.Surfaces, CommandImages: c.CommandImages}).validate(); err != nil {
		return err
	}
	for tool, surface := range c.Surfaces {
		if !slotIdentifier(tool) || (surface.ContentArg != "" && !slotIdentifier(surface.ContentArg)) {
			return fmt.Errorf("credential slots: invalid tool or content argument %q", tool)
		}
	}
	for _, group := range [][]string{c.EnvKeys, c.FileKeys} {
		for _, name := range group {
			if !slotIdentifier(name) {
				return fmt.Errorf("credential slots: invalid key %q", name)
			}
		}
	}
	for _, term := range c.KeyTerms {
		if !slotTerm(term) {
			return fmt.Errorf("credential slots: invalid term %q", term)
		}
	}
	for _, pair := range c.KeyTermPairs {
		if !slotTerm(pair[0]) || !slotTerm(pair[1]) {
			return fmt.Errorf("credential slots: pairs require two lowercase identifier segments")
		}
	}
	if err := validateSlotFlags(c.ValueFlags, true); err != nil {
		return err
	}
	return validateSlotImages(c.CommandImages)
}

func slotIdentifier(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("_.-", r) {
			return false
		}
	}
	return len(splitKeySegments(s)) > 0
}

func slotTerm(s string) bool {
	parts := splitKeySegments(s)
	return slotIdentifier(s) && len(parts) == 1 && parts[0] == s
}

func validateSlotFlags(flags []string, longOnly bool) error {
	if len(flags) > maxContributionEntries {
		return fmt.Errorf("credential slots: too many flags")
	}
	for _, flag := range flags {
		name, ok := strings.CutPrefix(flag, "--")
		if !ok && !longOnly {
			name, ok = strings.CutPrefix(flag, "-")
			ok = ok && len(name) == 1
		}
		if !ok || !slotIdentifier(name) || strings.HasPrefix(name, "-") {
			return fmt.Errorf("credential slots: invalid flag %q", flag)
		}
	}
	return nil
}

func validateSlotImages(images []CommandImage) error {
	for _, image := range images {
		if !slotIdentifier(image.Image) || (image.Value == "" && len(image.ValueFlags) == 0) {
			return fmt.Errorf("credential slots: command %q needs a basename and a value selector", image.Image)
		}
		if err := validateSlotFlags(image.RequireFlags, false); err != nil {
			return err
		}
		if err := validateSlotFlags(image.ValueFlags, false); err != nil {
			return err
		}
	}
	return nil
}

// Compile adds independent contributions to the bundled recognition baseline.
func Compile(contributions []Contribution) (*Inspector, error) {
	base, err := LoadBundled()
	if err != nil {
		return nil, err
	}
	cat := base.catalog
	for _, c := range contributions {
		if err := c.validate(); err != nil {
			return nil, err
		}
		cat.CommandImages = append(cat.CommandImages, cloneCommandImages(c.CommandImages)...)
		cat.ValueFlags = append(cat.ValueFlags, c.ValueFlags...)
		cat.EnvKeys = append(cat.EnvKeys, c.EnvKeys...)
		cat.FileKeys = append(cat.FileKeys, c.FileKeys...)
		cat.KeyTerms = append(cat.KeyTerms, c.KeyTerms...)
		cat.KeyTermPairs = append(cat.KeyTermPairs, c.KeyTermPairs...)
	}
	ins := newInspector(cat, base.set)
	for _, c := range contributions {
		for tool, surface := range c.Surfaces {
			ins.addSurface(tool, surface)
		}
	}
	return ins, nil
}

func cloneCommandImages(images []CommandImage) []CommandImage {
	out := make([]CommandImage, len(images))
	for n, image := range images {
		image.RequireFlags = append([]string(nil), image.RequireFlags...)
		image.ValueFlags = append([]string(nil), image.ValueFlags...)
		out[n] = image
	}
	return out
}
