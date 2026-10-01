package definition

import (
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/extpacks"
)

// LoadPackManifestsForCatalog compiles workflow units from catalog, or from
// the effective catalog when catalog is nil.
func LoadPackManifestsForCatalog(catalog *extpacks.EffectiveCatalog) (map[string]Manifest, map[string]ManifestSource, error) {
	if catalog == nil {
		resolved, err := extpacks.CatalogForConsumers()
		if err != nil {
			return nil, nil, err
		}
		catalog = resolved
	}
	return LoadManifestsFromCatalog(catalog)
}

// LoadManifestsFromCatalog compiles captured workflow units.
func LoadManifestsFromCatalog(catalog *extpacks.EffectiveCatalog) (map[string]Manifest, map[string]ManifestSource, error) {
	if catalog == nil {
		return nil, nil, fmt.Errorf("workflow manifests: effective catalog required")
	}
	return loadManifestsFromCatalog(catalog)
}

// catalogManifestsMemo caches parsed manifests by catalog revision.
var catalogManifestsMemo struct {
	mu      sync.Mutex
	entries map[string]catalogManifestSet
}

type catalogManifestSet struct {
	manifests map[string]Manifest
	sources   map[string]ManifestSource
}

func loadManifestsFromCatalog(catalog *extpacks.EffectiveCatalog) (map[string]Manifest, map[string]ManifestSource, error) {
	// Catalogs without revisions bypass the cache.
	key := strings.TrimSpace(catalog.Revision)
	if key != "" {
		catalogManifestsMemo.mu.Lock()
		cached, ok := catalogManifestsMemo.entries[key]
		catalogManifestsMemo.mu.Unlock()
		if ok {
			// Overlay merging mutates the returned maps.
			return maps.Clone(cached.manifests), maps.Clone(cached.sources), nil
		}
	}
	entries, sources, err := parsePackManifestsWithCatalog(catalog)
	if err != nil {
		return nil, nil, err
	}
	if key != "" {
		catalogManifestsMemo.mu.Lock()
		if catalogManifestsMemo.entries == nil {
			catalogManifestsMemo.entries = map[string]catalogManifestSet{}
		}
		catalogManifestsMemo.entries[key] = catalogManifestSet{
			manifests: maps.Clone(entries),
			sources:   maps.Clone(sources),
		}
		catalogManifestsMemo.mu.Unlock()
	}
	return entries, sources, nil
}

// parsePackManifestsWithCatalog compiles captured workflow units.
func parsePackManifestsWithCatalog(catalog *extpacks.EffectiveCatalog) (map[string]Manifest, map[string]ManifestSource, error) {
	entries := map[string]Manifest{}
	sources := map[string]ManifestSource{}
	for _, unitID := range catalog.LoadedUnitIDs() {
		id, ok := strings.CutPrefix(unitID, extpacks.WorkflowUnitIDPrefix)
		// Nested support units are not manifests.
		if !ok || id == "" || strings.HasPrefix(id, "_") || strings.Contains(id, "/") {
			continue
		}
		u, ok := catalog.Loaded[unitID]
		if !ok {
			continue
		}
		data, _, ok := catalog.UnitContent(unitID)
		if !ok {
			continue
		}
		at, _ := catalog.UnitPath(unitID)
		m, err := ParseManifestYAML(data)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", at, err)
		}
		key := ManifestKey(m.ID, m.Version)
		if _, dup := entries[key]; dup {
			return nil, nil, fmt.Errorf("workflow manifest %s from pack %s collides with an already-loaded manifest", key, u.WinnerPackID)
		}
		origin := OriginDisk
		if at.IsBundled() {
			origin = OriginBundled
		}
		entries[key] = m
		sources[key] = ManifestSource{Key: key, Path: at.String(), Origin: origin}
	}
	return entries, sources, nil
}
