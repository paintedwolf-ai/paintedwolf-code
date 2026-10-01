package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance/feedback"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestGateFeedbackSatisfyArmsSelectable ensures satisfy_auto / satisfy_coordinator
// arms are not dead catalog: at least one bundled phase lists the leaf and uses
// the matching EffectiveAdvancePolicy.
func TestGateFeedbackSatisfyArmsSelectable(t *testing.T) {
	t.Parallel()
	catalog, err := feedback.LoadGateFeedbackCatalog()
	contractcheck.FailErr(t, "LoadGateFeedbackCatalog", err)
	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "RegistryFromDirs", err)

	type leafAuth struct {
		auto        bool
		coordinator bool
	}
	selectable := map[string]*leafAuth{}
	for _, m := range reg.All() {
		for _, def := range m.PhaseDefs {
			auth := workflowdef.EffectiveAdvancePolicy(m, def)
			for _, leaf := range def.Gates {
				la := selectable[leaf]
				if la == nil {
					la = &leafAuth{}
					selectable[leaf] = la
				}
				switch auth {
				case workflowdef.AdvanceWhenGateMetAuto:
					la.auto = true
				case workflowdef.AdvanceWhenGateMetCoordinator:
					la.coordinator = true
				}
			}
		}
	}

	for _, id := range catalog.GateIDs() {
		def, ok := catalog.Def(id)
		if !ok {
			t.Fatalf("missing def %q", id)
		}
		la := selectable[id]
		if len(def.SatisfyAuto) > 0 {
			if la == nil || !la.auto {
				t.Fatalf("gate %q has satisfy_auto but no bundled phase uses auto advance with that leaf", id)
			}
		}
		if len(def.SatisfyCoordinator) > 0 {
			if la == nil || !la.coordinator {
				t.Fatalf("gate %q has satisfy_coordinator but no bundled phase uses coordinator advance with that leaf", id)
			}
		}
	}
}
