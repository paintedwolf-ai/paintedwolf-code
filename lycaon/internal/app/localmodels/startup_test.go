package localmodels_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/app/localmodels"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStartupFixturePreservesExplicitDecisionConfiguration(t *testing.T) {
	t.Setenv(bialy.EnvDevice, "cpu")
	t.Setenv(bialy.EnvModelDir, t.TempDir())
	s := localmodels.Start(t.Context(), true)
	if s.Runtime != nil || s.Generation() != nil || s.Issue != nil || s.Decision.Device != "cpu" {
		t.Fatalf("fixture launched or changed configured inference: %+v", s)
	}
	testutil.FailErr(t, "close absent generation", s.CloseGeneration(t.Context()))
	testutil.FailErr(t, "close absent resources", s.CloseResources(t.Context()))
}
