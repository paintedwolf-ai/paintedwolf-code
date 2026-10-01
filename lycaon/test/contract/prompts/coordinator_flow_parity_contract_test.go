package contract

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorFlowParity_ResolveTurnProfileMatchesEvaluator(t *testing.T) {
	t.Parallel()
	table, err := surface.LoadCoordinatorFlow()
	contractcheck.FailErr(t, "LoadCoordinatorFlow", err)

	for _, tc := range surface.AllFlowParityCases() {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			facts := surface.ComputeSurfaceFacts(tc.RunCtx, tc.Sess, tc.History, tc.State)

			profile := surface.ResolveTurnProfile(tc.RunCtx, tc.Sess, tc.History, tc.State)
			got, err := surface.EvaluateFlow(table, facts)
			contractcheck.FailErr(t, "EvaluateFlow", err)
			if profile.SurfaceID != got.SurfaceID {
				t.Fatalf("surface = %q want %q (rule %d)", profile.SurfaceID, got.SurfaceID, got.MatchedRuleIdx)
			}
			if len(profile.ModeRefs) == 0 {
				t.Fatal("empty modeRefs")
			}
			wantBase := profile.ModeRefs[0]
			if got.BaseModeRef != wantBase {
				t.Fatalf("base_mode_ref = %q want %q", got.BaseModeRef, wantBase)
			}
		})
	}
}

func TestCoordinatorFlowParity_caseCount(t *testing.T) {
	t.Parallel()
	n := len(surface.AllFlowParityCases())
	if n < 100 {
		t.Fatalf("parity matrix too small: %d cases", n)
	}
	fmt.Printf("coordinator flow parity matrix: %d cases\n", n)
}
