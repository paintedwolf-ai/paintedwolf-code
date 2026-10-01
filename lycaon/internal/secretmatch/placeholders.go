package secretmatch

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

// placeholderCatalog exempts exact documented example values from shape matches.
type placeholderCatalog struct {
	values map[string]string
}

type placeholderFile struct {
	Version      int `yaml:"version"`
	Placeholders []struct {
		Value  string `yaml:"value"`
		Source string `yaml:"source"`
	} `yaml:"placeholders"`
}

var (
	placeholderOnce sync.Once
	placeholderData placeholderCatalog
	placeholderErr  error
)

func bundledPlaceholders() (placeholderCatalog, error) {
	placeholderOnce.Do(func() {
		placeholderData, placeholderErr = loadPlaceholders()
	})
	return placeholderData, placeholderErr
}

func loadPlaceholders() (placeholderCatalog, error) {
	raw, err := config.Read(config.SecretPlaceholders)
	if err != nil {
		return placeholderCatalog{}, fmt.Errorf("secretmatch: placeholder catalog: %w", err)
	}
	return parsePlaceholders(raw)
}

func parsePlaceholders(raw []byte) (placeholderCatalog, error) {
	var file placeholderFile
	if err := config.DecodeYAML(raw, &file); err != nil {
		return placeholderCatalog{}, fmt.Errorf("secretmatch: placeholder catalog: %w", err)
	}
	if file.Version != 1 {
		return placeholderCatalog{}, fmt.Errorf("secretmatch: placeholder catalog version %d is not supported", file.Version)
	}
	catalog := placeholderCatalog{values: make(map[string]string, len(file.Placeholders))}
	for i, entry := range file.Placeholders {
		value := strings.TrimSpace(entry.Value)
		source := strings.TrimSpace(entry.Source)
		if value == "" || source == "" {
			return placeholderCatalog{}, fmt.Errorf("secretmatch: placeholder %d needs a value and a public source", i)
		}
		if _, dup := catalog.values[value]; dup {
			return placeholderCatalog{}, fmt.Errorf("secretmatch: placeholder %q is listed twice", value)
		}
		catalog.values[value] = source
	}
	return catalog, nil
}

// IsPlaceholder reports whether secret is a documented example value.
func (c placeholderCatalog) IsPlaceholder(secret string) bool {
	if len(c.values) == 0 {
		return false
	}
	_, ok := c.values[secret]
	return ok
}
