// Package extpacks discovers and resolves extension catalogs.
package extpacks

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
)

const (
	// StockPackIDPrefix is the pack id namespace for Painted Wolf stock packs.
	StockPackIDPrefix = "painted-wolf/"
	// PlatformPackID is the required stock platform pack.
	PlatformPackID = StockPackIDPrefix + "platform"

	// StockNamespace is the on-disk / embed root for Painted Wolf stock packs.
	StockNamespace = "config/packs/painted-wolf"

	// MarkerExtension is a FindModuleRoot marker under the platform pack.
	MarkerExtension = StockNamespace + "/platform/extension.yaml"
)

// ErrStockPack is returned when an operation is refused for a stock pack id.
var ErrStockPack = errors.New("stock pack")

// ErrPackNotInstalled reports a missing installed pack.
var ErrPackNotInstalled = errors.New("extension pack not installed")

// ErrLinkedSourceMissing reports an unreadable linked source.
var ErrLinkedSourceMissing = errors.New("linked extension source missing")

// ErrExtensionAPICompatibility reports an incompatible API constraint.
var ErrExtensionAPICompatibility = errors.New("extension API compatibility")

// IsStockPackID reports whether id is in the Painted Wolf stock pack namespace.
func IsStockPackID(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), StockPackIDPrefix)
}

// Pack is one discovered extension pack root.
type Pack struct {
	ID   string
	Root Source // bundled for stock packs, a host directory for cached/linked ones
}

// DiscoverStock lists contributing bundled packs. Authoring-tool and probe
// reach only — a loader compiles from resolved unit bytes.
func DiscoverStock() ([]Pack, error) {
	packs, err := discoverStockAll()
	if err != nil {
		return nil, err
	}
	if catalog := Active(); catalog != nil {
		return catalog.FilterPacks(packs), nil
	}
	return packs, nil
}

// DiscoverEffective resolves committed device state when no catalog is supplied.
func DiscoverEffective(catalog *EffectiveCatalog) ([]Pack, error) {
	if catalog == nil {
		resolved, err := CatalogForConsumers()
		if err != nil {
			return nil, err
		}
		catalog = resolved
	}
	if catalog.resolvedPacks != nil {
		all := make([]Pack, 0, len(catalog.resolvedPacks))
		for _, pack := range catalog.resolvedPacks {
			all = append(all, pack)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		return catalog.FilterPacks(all), nil
	}
	// Hand-assembled catalogs (tests) retain no resolve input; fall back to a
	// discovery walk filtered by the catalog's contributing set.
	stock, err := discoverStockAll()
	if err != nil {
		return nil, err
	}
	locked, err := DiscoverLockedContent(nil)
	if err != nil {
		return nil, err
	}
	byID := map[string]Pack{}
	for _, p := range stock {
		byID[p.ID] = p
	}
	for _, content := range locked {
		byID[content.Pack.ID] = content.Pack
	}
	out := make([]Pack, 0, len(byID))
	for _, p := range byID {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return catalog.FilterPacks(out), nil
}

// discoverStockAll lists every bundled pack.
func discoverStockAll() ([]Pack, error) {
	root := Bundled(config.StockPacks)
	ents, err := config.List(config.StockPacks)
	if err != nil {
		return nil, fmt.Errorf("extpacks: discover %s: %w", root, err)
	}
	var out []Pack
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		leaf := ent.Name()
		packRoot := root.Join(leaf)
		manifest := packRoot.Join(config.PackManifestName)
		man, err := ManifestAt(packRoot)
		if err != nil {
			return nil, fmt.Errorf("extpacks: read %s: %w", manifest, err)
		}
		id := man.ID
		out = append(out, Pack{ID: id, Root: packRoot})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) == 0 {
		return nil, fmt.Errorf("extpacks: no stock packs under %s", root)
	}
	return out, nil
}

// KindDirs lists authoring roots; runtime loaders use captured catalog bytes.
func KindDirs(packs []Pack, kind string) []Source {
	kind = strings.Trim(filepath.ToSlash(kind), "/")
	out := make([]Source, 0, len(packs))
	for _, p := range packs {
		dir := p.Root.Join(strings.Split(kind, "/")...)
		if dir.IsDir() {
			out = append(out, dir)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// PackFile is a unit file tagged with the pack that ships it.
type PackFile struct {
	PackID string
	Path   Source
}
