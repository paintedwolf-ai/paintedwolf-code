package definition

import (
	"strings"

	"github.com/lycaon/lycaon/internal/vocabulary"
)

// Built-in complete_when forms evaluated by GateEvaluator without the condition registry.
const (
	CompleteWhenGatesSatisfied = "gates_satisfied"
	CompleteWhenGateSatisfied  = "gate_satisfied:"
)

func IsKnownCompleteWhen(expr string) bool {
	leaves, err := CompletionLeaves(expr)
	if err != nil {
		return false
	}
	for _, leaf := range leaves {
		if !vocabulary.IsPredicate(leaf) {
			return false
		}
	}
	return true
}

// CollectManifestVocabulary returns unique complete_when and gate leaf ids from a manifest.
func CollectManifestVocabulary(m Manifest) (completeWhen []string, gateLeaves []string) {
	seenCW := map[string]struct{}{}
	seenGate := map[string]struct{}{}
	for _, p := range m.PhaseDefs {
		if cw := strings.TrimSpace(p.CompleteWhen); cw != "" {
			if _, ok := seenCW[cw]; !ok {
				seenCW[cw] = struct{}{}
				completeWhen = append(completeWhen, cw)
			}
		}
		for _, g := range p.Gates {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			if _, ok := seenGate[g]; !ok {
				seenGate[g] = struct{}{}
				gateLeaves = append(gateLeaves, g)
			}
		}
	}
	return completeWhen, gateLeaves
}
