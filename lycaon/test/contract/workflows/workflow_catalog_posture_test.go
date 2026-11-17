package contract

import (
	"testing"

	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestWorkflowCatalogSummaryPosturesValid(t *testing.T) {
	t.Parallel()
	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog failed", err)
	for key, m := range catalog {
		summary := m.Summary()
		if ip := m.InitialPosture; ip != "" {
			if summary.InitialPosture != ip {
				t.Errorf("%s: summary initial_posture = %q manifest = %q", key, summary.InitialPosture, ip)
			}
			if !sessionposture.ValidSessionPosture(ip) {
				t.Errorf("%s: invalid initial_posture %q", key, ip)
			}
		}
		if summary.InitialPosture != "" && !sessionposture.ValidSessionPosture(summary.InitialPosture) {
			t.Errorf("%s: summary exposes invalid posture %q", key, summary.InitialPosture)
		}
	}
}

func TestDefaultRegistrySummariesExposeValidPostures(t *testing.T) {
	t.Parallel()
	reg := contractcheck.CatalogRegistry(t)
	for _, summary := range reg.SummariesWithScopes(nil) {
		if summary.InitialPosture == "" {
			t.Errorf("workflow %s@%s missing initial_posture in catalog summary", summary.ID, summary.Version)
			continue
		}
		if !sessionposture.ValidSessionPosture(summary.InitialPosture) {
			t.Errorf("workflow %s@%s summary posture = %q invalid", summary.ID, summary.Version, summary.InitialPosture)
		}
	}
}
