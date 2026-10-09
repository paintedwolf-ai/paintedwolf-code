package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/profiles"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestBundledManifestCompleteWhenAndGatesKnown(t *testing.T) {
	t.Parallel()
	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog failed", err)
	for key, m := range catalog {
		completeWhen, gateLeaves := workflowdef.CollectManifestVocabulary(m)
		for _, cw := range completeWhen {
			if !workflowdef.IsKnownCompleteWhen(cw) {
				t.Errorf("%s: unknown complete_when %q", key, cw)
			}
		}
		for _, g := range gateLeaves {
			if !workflowdef.IsKnownGateLeaf(g) {
				t.Errorf("%s: unknown gate leaf %q", key, g)
			}
		}
	}
}

func TestWorkflowPhaseIDsAreNotSessionPostures(t *testing.T) {
	t.Parallel()
	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog failed", err)
	for key, m := range catalog {
		for _, phaseID := range m.Phases {
			if profiles.ValidSessionPosture(phaseID) {
				t.Errorf("%s: phase id %q must not be a session posture", key, phaseID)
			}
		}
	}
}
