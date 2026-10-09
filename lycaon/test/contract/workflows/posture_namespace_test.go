package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestPostureNamespaceDisjointFromWorkflowsAndPhases(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	postures := postureIDSet(profiles.AllSessionPostures())
	workflowIDs, phaseIDs := collectWorkflowNamespaces(t, root)

	for id := range postures {
		if _, ok := workflowIDs[id]; ok {
			t.Errorf("posture %q collides with workflow id", id)
		}
	}
	for id := range postures {
		if _, ok := phaseIDs[id]; ok {
			t.Errorf("posture %q collides with bundled phase id", id)
		}
	}
}

func TestPostureNamespaceDisjointFromEvidenceTypes(t *testing.T) {
	t.Parallel()
	postures := postureIDSet(profiles.AllSessionPostures())
	evidence := evidenceTypeIDSet()
	for id := range postures {
		if _, ok := evidence[id]; ok {
			t.Errorf("posture %q collides with evidence type id", id)
		}
	}
}

func postureIDSet(postures []api.SessionPosture) map[string]struct{} {
	out := make(map[string]struct{}, len(postures))
	for _, p := range postures {
		out[string(p)] = struct{}{}
	}
	return out
}

func collectWorkflowNamespaces(t *testing.T, root string) (workflowIDs, phaseIDs map[string]struct{}) {
	t.Helper()
	workflowIDs = map[string]struct{}{}
	phaseIDs = map[string]struct{}{}

	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog failed", err)
	for _, m := range catalog {
		workflowIDs[m.ID] = struct{}{}
		for _, p := range m.Phases {
			phaseIDs[p] = struct{}{}
		}
		for _, def := range m.PhaseDefs {
			phaseIDs[def.ID] = struct{}{}
		}
	}
	return workflowIDs, phaseIDs
}

func evidenceTypeIDSet() map[string]struct{} {
	types := evidence.AllGateTypes()
	out := make(map[string]struct{}, len(types))
	for _, e := range types {
		out[string(e)] = struct{}{}
	}
	return out
}
