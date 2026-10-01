package workflow

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/boolexpr"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// GatePredicateRef locates a gate leaf referenced from a shipped manifest.
type GatePredicateRef struct {
	ManifestID string
	PhaseID    string
	LeafID     string
}

// CollectManifestGatePredicates reads the gate leaves every resolved pack
// workflow manifest references.
func CollectManifestGatePredicates() ([]GatePredicateRef, error) {
	raw, _, err := workflowdef.LoadPackManifestsForCatalog(nil)
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

func gatePredicatesFromManifest(m workflowdef.Manifest) []GatePredicateRef {
	var out []GatePredicateRef
	for _, p := range m.PhaseDefs {
		for _, leaf := range decomposeGateExpression(p.CompleteWhen) {
			out = append(out, GatePredicateRef{ManifestID: m.ID, PhaseID: p.ID, LeafID: leaf})
		}
		for _, leaf := range decomposeGateExpression(p.ChildCompleteWhen) {
			out = append(out, GatePredicateRef{ManifestID: m.ID, PhaseID: p.ID, LeafID: leaf})
		}
		for _, leaf := range decomposeGateExpression(p.EntryWhen) {
			out = append(out, GatePredicateRef{ManifestID: m.ID, PhaseID: p.ID, LeafID: leaf})
		}
		for _, g := range p.Gates {
			g = strings.TrimSpace(g)
			if g != "" {
				out = append(out, GatePredicateRef{ManifestID: m.ID, PhaseID: p.ID, LeafID: g})
			}
		}
		for _, g := range p.ChildGates {
			g = strings.TrimSpace(g)
			if g != "" {
				out = append(out, GatePredicateRef{ManifestID: m.ID, PhaseID: p.ID, LeafID: g})
			}
		}
	}
	return out
}

func decomposeGateExpression(expr string) []string {
	expr = strings.TrimSpace(expr)
	if expr == "" || expr == workflowdef.CompleteWhenGatesSatisfied {
		return nil
	}
	if strings.HasPrefix(expr, workflowdef.CompleteWhenGateSatisfied) {
		leaf := strings.TrimSpace(strings.TrimPrefix(expr, workflowdef.CompleteWhenGateSatisfied))
		if leaf != "" {
			return []string{leaf}
		}
		return nil
	}
	if workflowdef.NeedsCompoundCompleteWhen(expr) {
		node, err := boolexpr.Parse(expr)
		if err != nil {
			return []string{expr}
		}
		return boolexpr.CollectIdents(node)
	}
	if workflowdef.IsKnownCompleteWhen(expr) || workflowdef.IsKnownGateLeaf(expr) {
		return []string{expr}
	}
	return nil
}
