package definition

import (
	"sort"
	"strings"
)

// CollectManifestGatePredicates reads the gate leaves every resolved pack
// workflow manifest references.
func CollectManifestGatePredicates() ([]GatePredicateRef, error) {
	raw, _, err := LoadPackManifestsForCatalog(nil)
	if err != nil {
		return nil, err
	}
	seen := map[string]GatePredicateRef{}
	for _, m := range raw {
		for _, ref := range gatePredicatesFromManifest(m) {
			key := ref.ManifestID + "|" + ref.PhaseID + "|" + ref.LeafID
			if _, ok := seen[key]; !ok {
				seen[key] = ref
			}
		}
	}
	out := make([]GatePredicateRef, 0, len(seen))
	for _, ref := range seen {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LeafID != out[j].LeafID {
			return out[i].LeafID < out[j].LeafID
		}
		if out[i].ManifestID != out[j].ManifestID {
			return out[i].ManifestID < out[j].ManifestID
		}
		return out[i].PhaseID < out[j].PhaseID
	})
	return out, nil
}

func gatePredicatesFromManifest(m Manifest) []GatePredicateRef {
	var out []GatePredicateRef
	for _, p := range m.PhaseDefs {
		for _, field := range p.PredicateFields() {
			for _, leaf := range field.Leaves {
				out = append(out, GatePredicateRef{ManifestID: m.ID, PhaseID: p.ID, LeafID: leaf})
			}
		}

	}
	return out
}

// UniqueGateLeafIDs returns sorted unique leaf ids from manifest gate predicates.
func UniqueGateLeafIDs(refs []GatePredicateRef) []string {
	seen := map[string]struct{}{}
	for _, ref := range refs {
		id := strings.TrimSpace(ref.LeafID)
		if id != "" {
			seen[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
