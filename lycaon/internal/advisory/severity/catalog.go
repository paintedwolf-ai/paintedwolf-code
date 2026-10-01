package severity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/config"
)

// CatalogPath is the bundled severity catalog written by scripts/build-advisory-severity.py.
const CatalogPath config.Rel = "runtime/scanners/advisory-severity.json"

// catalogEntry is one catalog row; the score is always derived from the vector.
type catalogEntry struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Vector string `json:"vector"`
	Source string `json:"source"`
}

// ParseCatalog validates catalog rows and resolves their base scores.
func ParseCatalog(data []byte) ([]Entry, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var rows []catalogEntry
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("parse advisory severity catalog: %w", err)
	}
	entries := make([]Entry, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for i, row := range rows {
		id := normalizeID(row.ID)
		if id == "" || id != row.ID {
			return nil, fmt.Errorf("advisory severity catalog row %d: id %q must be trimmed upper case", i, row.ID)
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("advisory severity catalog: duplicate id %s", id)
		}
		seen[id] = struct{}{}
		score, cvssType, ok := ParseVector(row.Vector)
		if !ok {
			return nil, fmt.Errorf("advisory severity catalog %s: invalid vector %q", id, row.Vector)
		}
		if cvssType != row.Type {
			return nil, fmt.Errorf("advisory severity catalog %s: type %s does not match vector %s", id, row.Type, cvssType)
		}
		if row.Source != SourceNVDCVSS && row.Source != SourceGHSACVSS {
			return nil, fmt.Errorf("advisory severity catalog %s: unknown source %q", id, row.Source)
		}
		entries = append(entries, Entry{ID: id, Type: cvssType, Vector: row.Vector, Score: score, Source: row.Source})
	}
	return entries, nil
}

var loadDefault = sync.OnceValues(func() (Resolver, error) {
	data, err := config.Read(CatalogPath)
	if err != nil {
		return nil, fmt.Errorf("read advisory severity catalog: %w", err)
	}
	entries, err := ParseCatalog(data)
	if err != nil {
		return nil, err
	}
	return NewResolver(entries...), nil
})

// Default returns the resolver over the bundled catalog, or why it could not load.
func Default() (Resolver, error) {
	return loadDefault()
}
