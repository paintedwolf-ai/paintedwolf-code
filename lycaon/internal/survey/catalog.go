package survey

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
)

// ProbeKind is a supported survey probe type.
type ProbeKind string

const (
	ProbeGrep    ProbeKind = "grep"
	ProbeFind    ProbeKind = "find"
	ProbeListDir ProbeKind = "list_dir"
)

// Probe is one catalog probe definition.
type Probe struct {
	Kind     ProbeKind `yaml:"kind"`
	Path     string    `yaml:"path"`
	Label    string    `yaml:"label"`
	Pattern  string    `yaml:"pattern,omitempty"`
	NameGlob string    `yaml:"name_glob,omitempty"`
	Priority int       `yaml:"priority"`
	// EmitCap bounds retained path records. Zero is unlimited.
	EmitCap int `yaml:"emit_cap,omitempty"`
}

// Bundle is a named survey probe bundle.
type Bundle struct {
	ID          string  `yaml:"id"`
	Description string  `yaml:"description"`
	Probes      []Probe `yaml:"probes"`
}

// Catalog holds loaded survey bundles keyed by id.
type Catalog struct {
	Bundles map[string]Bundle
}

// CatalogDir returns the bundled survey catalog directory under configRoot.
func CatalogDir() extpacks.Source {
	return extpacks.Bundled(config.PlatformShared.Join("survey"))
}

// LoadCatalog reads every *.yaml bundle from dir.
func LoadCatalog(dir extpacks.Source) (*Catalog, error) {
	entries, err := dir.List()
	if err != nil {
		return nil, fmt.Errorf("read survey catalog %s: %w", dir, err)
	}
	cat := &Catalog{Bundles: make(map[string]Bundle)}
	for _, ent := range entries {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".yaml" {
			continue
		}
		path := dir.Join(ent.Name())
		bundle, err := loadBundleFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ent.Name(), err)
		}
		if _, dup := cat.Bundles[bundle.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate bundle id %q", ent.Name(), bundle.ID)
		}
		cat.Bundles[bundle.ID] = bundle
	}
	if len(cat.Bundles) == 0 {
		return nil, fmt.Errorf("survey catalog: no bundles in %s", dir)
	}
	return cat, nil
}

// MergeOverlay appends probes from project overlay bundles (append-only by id).
func MergeOverlay(cat *Catalog, overlayDir extpacks.Source) error {
	if cat == nil {
		return fmt.Errorf("survey catalog: nil catalog")
	}
	entries, err := overlayDir.List()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read survey overlay %s: %w", overlayDir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".yaml" {
			continue
		}
		path := overlayDir.Join(ent.Name())
		overlay, err := loadBundleFile(path)
		if err != nil {
			return fmt.Errorf("overlay %s: %w", ent.Name(), err)
		}
		base, ok := cat.Bundles[overlay.ID]
		if !ok {
			cat.Bundles[overlay.ID] = overlay
			continue
		}
		base.Probes = append(base.Probes, overlay.Probes...)
		cat.Bundles[overlay.ID] = base
	}
	return nil
}

func loadBundleFile(path extpacks.Source) (Bundle, error) {
	data, err := path.Read()
	if err != nil {
		return Bundle{}, err
	}
	var bundle Bundle
	if err := config.DecodeYAML(data, &bundle); err != nil {
		return Bundle{}, fmt.Errorf("parse: %w", err)
	}
	bundle.ID = strings.TrimSpace(bundle.ID)
	if bundle.ID == "" {
		return Bundle{}, fmt.Errorf("bundle missing id")
	}
	if len(bundle.Probes) == 0 {
		return Bundle{}, fmt.Errorf("bundle %q: no probes", bundle.ID)
	}
	for i := range bundle.Probes {
		if err := normalizeProbe(&bundle.Probes[i], bundle.ID, i); err != nil {
			return Bundle{}, err
		}
	}
	return bundle, nil
}

func normalizeProbe(p *Probe, bundleID string, idx int) error {
	p.Kind = ProbeKind(strings.TrimSpace(string(p.Kind)))
	switch p.Kind {
	case ProbeGrep, ProbeFind, ProbeListDir:
	default:
		return fmt.Errorf("bundle %q probe %d: unknown kind %q", bundleID, idx, p.Kind)
	}
	p.Path = strings.TrimSpace(p.Path)
	if p.Path == "" {
		p.Path = "."
	}
	p.Label = strings.TrimSpace(p.Label)
	if p.Label == "" {
		p.Label = fmt.Sprintf("%s_%d", p.Kind, idx+1)
	}
	if p.Kind == ProbeGrep && strings.TrimSpace(p.Pattern) == "" {
		return fmt.Errorf("bundle %q probe %q: grep requires pattern", bundleID, p.Label)
	}
	if p.Kind == ProbeFind {
		p.NameGlob = strings.TrimSpace(p.NameGlob)
		if p.NameGlob == "" {
			p.NameGlob = "**/*"
		}
	}
	if p.EmitCap < 0 {
		return fmt.Errorf("bundle %q probe %q: emit_cap must be >= 0", bundleID, p.Label)
	}
	return nil
}

// Clone returns a deep copy of the catalog suitable for per-call overlay merge.
func (c *Catalog) Clone() *Catalog {
	if c == nil {
		return nil
	}
	out := &Catalog{Bundles: make(map[string]Bundle, len(c.Bundles))}
	for id, b := range c.Bundles {
		nb := b
		nb.Probes = append([]Probe(nil), b.Probes...)
		out.Bundles[id] = nb
	}
	return out
}

// BundleIDs returns sorted bundle ids.
func (c *Catalog) BundleIDs() []string {
	if c == nil {
		return nil
	}
	ids := make([]string, 0, len(c.Bundles))
	for id := range c.Bundles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
