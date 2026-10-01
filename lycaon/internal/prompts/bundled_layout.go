package prompts

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/promptunit"
)

// BundledLayout resolves templates through the effective catalog.
type BundledLayout struct {
	// Catalog gates unit resolution. Nil resolves committed device state.
	Catalog *extpacks.EffectiveCatalog
}

// DefaultBundledLayout returns a layout backed by the process Active catalog.
func DefaultBundledLayout() BundledLayout { return BundledLayout{} }

// ErrTemplateNotEffective marks a disabled or conflicted unit.
var ErrTemplateNotEffective = errors.New("template unit not in the effective catalog")

// kindForRef maps a template prefix to its catalog kind.
func kindForRef(ref string) (kind, rel string, ok bool) {
	for _, m := range []struct{ prefix, kind string }{
		{"agents/", "agents/prompts"},
		{"partials/", "shared/partials"},
		{"units/", "shared/units"},
		{"archetypes/", "shared/archetypes"},
		{"kicks/", "guidance"},
		{"inject/", "guidance"},
		{"guidance/", "guidance"},
	} {
		if strings.HasPrefix(ref, m.prefix) {
			return m.kind, strings.TrimPrefix(ref, m.prefix), true
		}
	}
	return "", "", false
}

// ReadBundled returns effective bytes and their source.
func (l BundledLayout) ReadBundled(ref string) ([]byte, extpacks.Source, error) {
	ref = path.Clean(strings.TrimSpace(ref))
	kind, rel, ok := kindForRef(ref)
	if !ok {
		// Template references require a catalog prefix.
		return nil, extpacks.Source{}, fmt.Errorf("template %q has no prefix — refs are guidance/<stem>, agents/<file>, partials/<stem>, or archetypes/<stem> (a binding's render: %s is guidance/%s here)", ref, ref, ref)
	}
	eff := l.Catalog
	if eff == nil {
		resolved, err := extpacks.CatalogForConsumers()
		if err != nil {
			return nil, extpacks.Source{}, err
		}
		eff = resolved
	}
	return findInKind(kind, rel, eff)
}

// findInKind resolves captured bytes for one catalog unit.
func findInKind(kind, rel string, eff *extpacks.EffectiveCatalog) ([]byte, extpacks.Source, error) {
	rel = strings.TrimPrefix(path.Clean(rel), "/")
	for _, candidate := range unitRelCandidates(kind, rel) {
		// Template refs address a unit by path, which only the kinds that are not
		// provider-scoped can be. Those return "" here and fall through.
		id := extpacks.UnitIDFor("", candidate)
		if id == "" {
			continue
		}
		if content, _, ok := eff.UnitContent(id); ok {
			at, _ := eff.UnitPath(id)
			if kind == "shared/units" {
				// A unit's front matter is catalog data, not template text.
				content = promptunit.Body(content)
			}
			return content, at, nil
		}
		if len(eff.InspectContributions(id)) == 0 {
			continue
		}
		return nil, extpacks.Source{}, fmt.Errorf("template %s: %w — re-enable it under Settings → Extensions", id, ErrTemplateNotEffective)
	}
	if strings.HasPrefix(path.Base(rel), "_") {
		return readReservedPartial(kind, rel, eff)
	}
	return nil, extpacks.Source{}, fmt.Errorf("template %s/%s not found in the effective catalog", kind, rel)
}

// readReservedPartial resolves fixed `_`-prefixed includes.
// Reserved names require one contributing source.
func readReservedPartial(kind, rel string, eff *extpacks.EffectiveCatalog) ([]byte, extpacks.Source, error) {
	packs, err := extpacks.DiscoverEffective(eff)
	if err != nil {
		return nil, extpacks.Source{}, err
	}
	var hits []extpacks.Source
	for _, p := range packs {
		base := p.Root.Join(strings.Split(kind, "/")...)
		for _, at := range kindCandidates(base, rel) {
			if _, err := at.Stat(); err == nil {
				hits = append(hits, at)
			}
		}
	}
	switch len(hits) {
	case 0:
		return nil, extpacks.Source{}, fmt.Errorf("template %s/%s not found in contributing packs", kind, rel)
	case 1:
		data, err := hits[0].Read()
		if err != nil {
			return nil, extpacks.Source{}, err
		}
		return data, hits[0], nil
	default:
		return nil, extpacks.Source{}, fmt.Errorf("template %s/%s contributed by multiple packs", kind, rel)
	}
}

// kindCandidates are the files a ref may name: the literal path, and — for an
// extension-less stem — the two unit file types a pack may ship it as.
func kindCandidates(base extpacks.Source, rel string) []extpacks.Source {
	out := []extpacks.Source{base.Join(rel)}
	if !strings.Contains(rel, ".") {
		out = append(out, base.Join(rel+".md"), base.Join(rel+".yaml"))
	}
	return out
}

// unitRelCandidates mirrors the extension-less refs the pack walk accepts.
func unitRelCandidates(kind, rel string) []string {
	base := strings.Trim(kind, "/") + "/" + rel
	if strings.Contains(rel, ".") {
		return []string{base}
	}
	return []string{base + ".md", base + ".yaml"}
}
