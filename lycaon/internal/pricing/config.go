package pricing

import (
	"fmt"
	"github.com/lycaon/lycaon/config"
	"strings"

	"github.com/lycaon/lycaon/internal/catalogruntime"
)

// SourceConfig is one catalog entry from pricing-sources.yaml.
type SourceConfig struct {
	ID    string `yaml:"id"`
	Kind  string `yaml:"kind"`
	Label string `yaml:"label"`
	URL   string `yaml:"url"`
}

// SourcesConfig is the bundled feed catalog.
type SourcesConfig struct {
	Sources []SourceConfig `yaml:"sources"`
}

// LoadSourcesConfig reads and validates a pricing-sources.yaml file.
func LoadSourcesConfig() (SourcesConfig, error) {
	raw, err := config.Read(config.PricingSources)
	if err != nil {
		return SourcesConfig{}, err
	}
	return ParseSourcesConfig(raw)
}

// ParseSourcesConfig validates catalog bytes.
func ParseSourcesConfig(raw []byte) (SourcesConfig, error) {
	var cfg SourcesConfig
	if err := config.DecodeYAML(raw, &cfg); err != nil {
		return SourcesConfig{}, fmt.Errorf("pricing: decode sources: %w", err)
	}
	catalog, err := assembleSourceCatalog(cfg.Sources)
	if err != nil {
		return SourcesConfig{}, err
	}
	cfg.Sources = sourceSpecs(catalog)
	return cfg, nil
}

// assembleSourceCatalog validates pricing's domain schema and translates it
// into the shared catalog kernel. The returned slice retains source-file order,
// which determines the default selected source.
func assembleSourceCatalog(sources []SourceConfig) (*catalogruntime.Catalog[SourceConfig], error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("pricing: sources catalog is empty")
	}
	items := make([]catalogruntime.Item[SourceConfig], 0, len(sources))
	for i, source := range sources {
		source.ID = strings.TrimSpace(source.ID)
		source.Kind = strings.TrimSpace(source.Kind)
		source.Label = strings.TrimSpace(source.Label)
		source.URL = strings.TrimSpace(source.URL)
		if source.ID == "" {
			return nil, fmt.Errorf("pricing: source[%d] missing id", i)
		}
		if source.Kind == "" {
			return nil, fmt.Errorf("pricing: source %q missing kind", source.ID)
		}
		if !knownKind(source.Kind) {
			return nil, fmt.Errorf("%w: %q", ErrUnknownKind, source.Kind)
		}
		// Shared metadata projections have no feed URL.
		if source.URL == "" && source.Kind != kindModelsDev {
			return nil, fmt.Errorf("pricing: source %q missing url", source.ID)
		}
		if source.Label == "" {
			source.Label = source.ID
		}
		items = append(items, catalogruntime.Item[SourceConfig]{
			ID: source.ID, Spec: source,
		})
	}
	catalog, err := catalogruntime.Assemble(
		[]catalogruntime.Layer[SourceConfig]{{
			Name: "bundled pricing sources", Items: items,
		}},
		func(current catalogruntime.Item[SourceConfig], exists bool, incoming catalogruntime.Item[SourceConfig]) (catalogruntime.Item[SourceConfig], error) {
			if exists {
				return current, fmt.Errorf("pricing: duplicate source id %q", incoming.ID)
			}
			return incoming, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return catalog, nil
}

func sourceSpecs(catalog *catalogruntime.Catalog[SourceConfig]) []SourceConfig {
	items := catalog.Items()
	sources := make([]SourceConfig, len(items))
	for i, item := range items {
		sources[i] = item.Spec
	}
	return sources
}

func knownKind(kind string) bool {
	switch kind {
	case kindModelsDev, kindLitellm, kindAIPricingFYI:
		return true
	}
	return false
}
