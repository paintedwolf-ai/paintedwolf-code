package contract

import (
	"testing"

	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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
