package contract

import (
	"testing"

	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestWorkflowPhaseIDsAreNotSessionPostures(t *testing.T) {
	t.Parallel()
	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog failed", err)
	for key, m := range catalog {
		for _, phaseID := range m.Phases {
			if sessionposture.ValidSessionPosture(phaseID) {
				t.Errorf("%s: phase id %q must not be a session posture", key, phaseID)
			}
		}
	}
}
