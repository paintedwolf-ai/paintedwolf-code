// Package surfacecatalog loads coordinator tool-surface declarations.
package surfacecatalog

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// NoFolderAllowlistID identifies the reserved rootless floor row.
const NoFolderAllowlistID = "no_folder_allowlist"

// Card is surface banner copy.
type Card struct {
	Label string `yaml:"label"`
	Rule  string `yaml:"rule"`
}

// Surface is a coordinator surface declaration.
type Surface struct {
	ActivityLabel string `yaml:"activity_label"`
	ModeRef       string `yaml:"mode_ref"`
	Card          Card   `yaml:"card"`
	Exit          string `yaml:"exit"`
	// ProseFinish lets a turn on this surface end with grounded prose to the person.
	ProseFinish bool `yaml:"prose_finish"`
	// Floor is offered on every model call.
	Floor []string `yaml:"floor"`
	// Loadable names the tools the turn may add; whatever is not loaded stays
	// requestable through request_tools.
	Loadable []string `yaml:"loadable"`
}

// LoadableTools returns the tools a surface can load, with resource families
// expanded so every member shares one availability decision.
func (s Surface) LoadableTools() []string {
	return toolcontract.ExpandFamilies(s.Loadable)
}

// FloorTools returns the floor offered on every call, families expanded.
func (s Surface) FloorTools() []string {
	return toolcontract.ExpandFamilies(s.Floor)
}

// Catalog is the validated coordinator surface catalog.
type Catalog struct {
	rows map[string]Surface
}

var decoded struct {
	mu      sync.Mutex
	raw     []byte
	catalog Catalog
}

// Load reads and validates the coordinator surface catalog.
func Load() (Catalog, error) {
	data, err := config.Read(config.CoordinatorSurface)
	if err != nil {
		return Catalog{}, fmt.Errorf("read coordinator surfaces: %w", err)
	}
	decoded.mu.Lock()
	defer decoded.mu.Unlock()
	if decoded.catalog.rows != nil && bytes.Equal(decoded.raw, data) {
		return decoded.catalog, nil
	}
	var rows map[string]Surface
	if err := config.DecodeYAML(data, &rows); err != nil {
		return Catalog{}, fmt.Errorf("parse coordinator surfaces: %w", err)
	}
	catalog, err := validate(rows)
	if err != nil {
		return Catalog{}, err
	}
	decoded.raw = append([]byte(nil), data...)
	decoded.catalog = catalog
	return catalog, nil
}

func validate(rows map[string]Surface) (Catalog, error) {
	if len(rows) == 0 {
		return Catalog{}, fmt.Errorf("coordinator surfaces: empty after parse (%s)", config.CoordinatorSurface)
	}
	out := Catalog{rows: make(map[string]Surface, len(rows))}
	for rawID, row := range rows {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return Catalog{}, fmt.Errorf("coordinator surfaces: empty surface id")
		}
		var err error
		row.Floor, err = normalizeNames(id, "floor", row.Floor)
		if err != nil {
			return Catalog{}, err
		}
		row.Loadable, err = normalizeNames(id, "loadable", row.Loadable)
		if err != nil {
			return Catalog{}, err
		}
		if id == NoFolderAllowlistID {
			if len(row.Floor) == 0 {
				return Catalog{}, fmt.Errorf("coordinator surfaces: missing or empty %s in %s", NoFolderAllowlistID, config.CoordinatorSurface)
			}
			if len(row.Loadable) > 0 {
				return Catalog{}, fmt.Errorf("coordinator surfaces: %s carries only a floor", NoFolderAllowlistID)
			}
			out.rows[id] = row
			continue
		}
		if strings.TrimSpace(row.ModeRef) == "" {
			return Catalog{}, fmt.Errorf("coordinator surface %q: mode_ref is required", id)
		}
		floor := nameSet(toolcontract.ExpandFamilies(row.Floor))
		for _, name := range row.LoadableTools() {
			if floor[name] {
				return Catalog{}, fmt.Errorf("coordinator surface %q: tool %q is both on the floor and loadable", id, name)
			}
		}
		if len(row.Loadable) > 0 && !floor["request_tools"] {
			return Catalog{}, fmt.Errorf("coordinator surface %q: loadable tools require request_tools on the floor", id)
		}
		out.rows[id] = row
	}
	if _, ok := out.rows[NoFolderAllowlistID]; !ok {
		return Catalog{}, fmt.Errorf("coordinator surfaces: missing or empty %s in %s", NoFolderAllowlistID, config.CoordinatorSurface)
	}
	return out, nil
}

func normalizeNames(surfaceID, field string, names []string) ([]string, error) {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("coordinator surface %q: %s contains an empty name", surfaceID, field)
		}
		if seen[name] {
			return nil, fmt.Errorf("coordinator surface %q: %s repeats %q", surfaceID, field, name)
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

func nameSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

// Surface returns one coordinator surface declaration.
func (c Catalog) Surface(id string) (Surface, error) {
	id = strings.TrimSpace(id)
	row, ok := c.rows[id]
	if !ok || id == NoFolderAllowlistID {
		return Surface{}, fmt.Errorf("unknown coordinator surface %q", id)
	}
	row.Floor = append([]string(nil), row.Floor...)
	row.Loadable = append([]string(nil), row.Loadable...)
	return row, nil
}

// SurfaceIDs returns the sorted coordinator surface ids.
func (c Catalog) SurfaceIDs() []string {
	out := make([]string, 0, len(c.rows)-1)
	for id := range c.rows {
		if id != NoFolderAllowlistID {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// NoFolderAllowlist returns the sorted rootless floor.
func (c Catalog) NoFolderAllowlist() []string {
	row := c.rows[NoFolderAllowlistID]
	out := append([]string(nil), row.Floor...)
	sort.Strings(out)
	return out
}
