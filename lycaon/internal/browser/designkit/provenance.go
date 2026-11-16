package designkit

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

// FontProvenance records one bundled font's source and license.
type FontProvenance struct {
	Family      string `yaml:"family"`
	File        string `yaml:"file"`
	License     string `yaml:"license"`
	LicenseFile string `yaml:"license_file"`
	Source      string `yaml:"source"`
	Upstream    string `yaml:"upstream"`
	Copyright   string `yaml:"copyright"`
}

// IconProvenance records the bundled icon catalog's source and license.
type IconProvenance struct {
	License     string `yaml:"license"`
	LicenseFile string `yaml:"license_file"`
	Upstream    string `yaml:"upstream"`
	Package     string `yaml:"package"`
	Copyright   string `yaml:"copyright"`
	Note        string `yaml:"note"`
}

type provenanceDoc struct {
	KitVersion int              `yaml:"kit_version"`
	Origin     string           `yaml:"origin"`
	Fonts      []FontProvenance `yaml:"fonts"`
	Icons      IconProvenance   `yaml:"icons"`
}

var loadProvenance = sync.OnceValues(func() (map[string]FontProvenance, error) {
	data, err := readEmbedded("provenance.yaml")
	if err != nil {
		return nil, fmt.Errorf("read provenance.yaml: %w", err)
	}
	var doc provenanceDoc
	if err := config.DecodeYAML(data, &doc); err != nil {
		return nil, fmt.Errorf("parse provenance.yaml: %w", err)
	}
	if doc.KitVersion != 1 {
		return nil, fmt.Errorf("provenance kit_version %d is unsupported", doc.KitVersion)
	}
	if strings.TrimSpace(doc.Origin) == "" {
		return nil, fmt.Errorf("provenance origin is required")
	}
	if strings.TrimSpace(doc.Icons.License) == "" || strings.TrimSpace(doc.Icons.LicenseFile) == "" {
		return nil, fmt.Errorf("icon provenance is incomplete")
	}
	if _, err := readEmbedded(doc.Icons.LicenseFile); err != nil {
		return nil, fmt.Errorf("read icon license: %w", err)
	}
	out := make(map[string]FontProvenance, len(doc.Fonts))
	for _, entry := range doc.Fonts {
		out[entry.Family] = entry
	}
	return out, nil
})

// FontProvenanceFor returns the record for family.
func FontProvenanceFor(family string) (FontProvenance, error) {
	byFamily, err := loadProvenance()
	if err != nil {
		return FontProvenance{}, err
	}
	entry, ok := byFamily[family]
	if !ok {
		return FontProvenance{}, fmt.Errorf("no provenance for font %q", family)
	}
	return entry, nil
}
