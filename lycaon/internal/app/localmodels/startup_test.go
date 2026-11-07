package localmodels_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/app/localmodels"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
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

func TestStartupFailurePreservesCauseWithoutOfferingRuntimeRepair(t *testing.T) {
	for _, test := range []struct {
		issue error
		state string
	}{
		{fmt.Errorf("allocate: %w", inference.ErrCapacity), "insufficient_memory"},
		{errors.New("inventory unavailable"), "unavailable"},
	} {
		s := localmodels.Startup{Issue: test.issue}
		availability := s.Generation()
		if availability.Runtime != nil || availability.Initial.State != nativegen.ReadinessState(test.state) || availability.Initial.Action != "none" || availability.Initial.Detail != test.issue.Error() {
			t.Fatalf("incorrect startup projection: %+v", availability)
		}
	}
}

func TestInsufficientPhysicalRAMHasCapacityIdentity(t *testing.T) {
	_, err := localmodels.AllocateResources(4<<30, inference.DeviceFacts{}, bialy.MemoryPlan{})
	if !errors.Is(err, inference.ErrCapacity) {
		t.Fatalf("physical RAM failure lost capacity identity: %v", err)
	}
}
