package extpacks

import (
	"fmt"
	"sort"
)

// LoadedUnitIDs returns sorted ids present in the effective (loaded) set.
func (e *EffectiveCatalog) LoadedUnitIDs() []string {
	if e == nil {
		return nil
	}
	out := make([]string, 0, len(e.Loaded))
	for id := range e.Loaded {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// UnitContent returns unit bytes and winner pack id when id is loaded.
func (e *EffectiveCatalog) UnitContent(id string) (content []byte, packID string, ok bool) {
	if e == nil {
		return nil, "", false
	}
	u, ok := e.Loaded[id]
	if !ok {
		return nil, "", false
	}
	return u.Content, u.WinnerPackID, true
}

// UnitPath returns the on-disk path of the winning contribution for id, for loaders
// that resolve a template or catalog file by path rather than by body.
func (e *EffectiveCatalog) UnitPath(id string) (Source, bool) {
	if e == nil {
		return Source{}, false
	}
	u, ok := e.Loaded[id]
	if !ok {
		return Source{}, false
	}
	for _, p := range u.Contributions {
		if p.PackID == u.WinnerPackID {
			return p.Path, true
		}
	}
	return Source{}, false
}

// HasLoaded reports whether unitID won and is loadable.
func (e *EffectiveCatalog) HasLoaded(unitID string) bool {
	if e == nil {
		return false
	}
	_, ok := e.Loaded[unitID]
	return ok
}

// ContributingPackIDs returns pack ids that passed gates and are enabled.
func (e *EffectiveCatalog) ContributingPackIDs() []string {
	if e == nil {
		return nil
	}
	var out []string
	for _, p := range e.Packs {
		if p.Contributing {
			out = append(out, p.ID)
		}
	}
	sort.Strings(out)
	return out
}

// PackContributed reports whether packID is contributing.
func (e *EffectiveCatalog) PackContributed(packID string) bool {
	if e == nil {
		return false
	}
	for _, p := range e.Packs {
		if p.ID == packID {
			return p.Contributing
		}
	}
	return false
}

// PackDependsOn reports an explicit resolved dependency of a contributing
// pack. Transitive presence is insufficient for semantic calls.
func (e *EffectiveCatalog) PackDependsOn(packID, dependencyID string) bool {
	if e == nil || packID == dependencyID {
		return packID == dependencyID
	}
	for _, pack := range e.Packs {
		if pack.ID != packID || !pack.Contributing {
			continue
		}
		_, ok := pack.Dependencies[dependencyID]
		return ok && e.PackContributed(dependencyID)
	}
	return false
}

// StockAuthority requires both the stock namespace and bundled provenance.
func (e *EffectiveCatalog) StockAuthority(packID string) bool {
	if e == nil || !IsStockPackID(packID) {
		return false
	}
	for _, p := range e.Packs {
		if p.ID == packID {
			return p.Bundled
		}
	}
	return false
}

// FilterPacks returns discovered packs that are contributing after resolve.
func (e *EffectiveCatalog) FilterPacks(all []Pack) []Pack {
	if e == nil {
		return all
	}
	allow := map[string]bool{}
	for _, id := range e.ContributingPackIDs() {
		allow[id] = true
	}
	var out []Pack
	for _, p := range all {
		if allow[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

// InspectContributions returns every contribution body for unitID (including disabled/conflict).
func (e *EffectiveCatalog) InspectContributions(unitID string) []UnitContribution {
	if e == nil {
		return nil
	}
	u, ok := e.Units[unitID]
	if !ok {
		return nil
	}
	return append([]UnitContribution(nil), u.Contributions...)
}

// BootError rejects an incoherent platform or OAR policy set.
func (e *EffectiveCatalog) BootError() error {
	if e == nil {
		return fmt.Errorf("extension packs: no effective catalog (resolve not run)")
	}
	if err := e.ValidateOARPolicies(); err != nil {
		return err
	}
	if e.PackContributed(PlatformPackID) {
		return nil
	}
	return fmt.Errorf("extension packs: %s is not contributing — session cannot boot (enable the platform pack or clear the disable)", PlatformPackID)
}
