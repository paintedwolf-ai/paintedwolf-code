package sandbox

import (
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"
	"sync"
	"weak"

	"github.com/lycaon/lycaon/internal/extpacks"
)

const toolProfileUnitPrefix = "tools/profiles/"

// toolProfilesCache holds catalogs weakly: an entry goes when its catalog is collected.
var (
	toolProfilesCacheMu sync.RWMutex
	toolProfilesCache   = map[weak.Pointer[extpacks.EffectiveCatalog]][]ToolProfile{}
)

func cloneToolProfiles(in []ToolProfile) []ToolProfile {
	out := make([]ToolProfile, len(in))
	for i, p := range in {
		out[i] = ToolProfile{
			ID:             p.ID,
			Description:    p.Description,
			Tools:          maps.Clone(p.Tools),
			StickyTools:    maps.Clone(p.StickyTools),
			DeferredTools:  maps.Clone(p.DeferredTools),
			DenyTools:      slices.Clone(p.DenyTools),
			MCPDeny:        slices.Clone(p.MCPDeny),
			ReadGlobs:      slices.Clone(p.ReadGlobs),
			WriteGlobs:     slices.Clone(p.WriteGlobs),
			WaitConditions: slices.Clone(p.WaitConditions),
			CommandEffects: p.CommandEffects,
		}
	}
	return out
}

// LoadToolProfiles loads path-scopes.yaml and tool profile units from every
// contributing pack — stock and installed — through the process catalog
// (resolving committed device state when none is active yet).
func LoadToolProfiles() ([]ToolProfile, error) {
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, err
	}
	return LoadToolProfilesWithCatalog(catalog)
}

// LoadToolProfilesWithCatalog compiles tool profiles from the resolved
// winners' captured bytes; the view never rereads pack files after resolve.
func LoadToolProfilesWithCatalog(catalog *extpacks.EffectiveCatalog) ([]ToolProfile, error) {
	if catalog == nil {
		return nil, fmt.Errorf("tool profiles: effective catalog required")
	}
	key := weak.Make(catalog)
	toolProfilesCacheMu.RLock()
	if cached, ok := toolProfilesCache[key]; ok {
		toolProfilesCacheMu.RUnlock()
		return cloneToolProfiles(cached), nil
	}
	toolProfilesCacheMu.RUnlock()

	// Path scopes stay a fixed platform file: they are pack content, not a
	// provide unit, and a pack cannot widen the scopes its own profiles bind to.
	scopes, err := LoadPathScopes()
	if err != nil {
		return nil, fmt.Errorf("path scopes: %w", err)
	}
	var profiles []ToolProfile
	seen := map[string]string{}
	for _, unitID := range catalog.LoadedUnitIDs() {
		if !strings.HasPrefix(unitID, toolProfileUnitPrefix) {
			continue
		}
		at, _ := catalog.UnitPath(unitID)
		// The tools root inventories .yml/.md siblings too; profiles are the
		// .yaml documents only, matching the authored layout.
		if !strings.HasSuffix(at.String(), ".yaml") {
			continue
		}
		content, _, ok := catalog.UnitContent(unitID)
		if !ok {
			continue
		}
		p, err := ParseToolProfile(content, scopes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		id := strings.TrimSpace(p.ID)
		if id == "" {
			id = strings.TrimPrefix(unitID, toolProfileUnitPrefix)
		}
		if prev, ok := seen[id]; ok {
			return nil, fmt.Errorf("duplicate tool profile %q in %s and %s", id, prev, at)
		}
		seen[id] = at.String()
		profiles = append(profiles, p)
	}
	toolProfilesCacheMu.Lock()
	_, existed := toolProfilesCache[key]
	toolProfilesCache[key] = profiles
	toolProfilesCacheMu.Unlock()
	if !existed {
		runtime.AddCleanup(catalog, func(k weak.Pointer[extpacks.EffectiveCatalog]) {
			toolProfilesCacheMu.Lock()
			delete(toolProfilesCache, k)
			toolProfilesCacheMu.Unlock()
		}, key)
	}
	return cloneToolProfiles(profiles), nil
}
