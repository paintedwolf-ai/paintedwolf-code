package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
)

func TestCoordinatorContextDietTurnProfileMatrix(t *testing.T) {
	t.Parallel()
	for _, row := range ContextDietMatrix {
		t.Run(row.Name, func(t *testing.T) {
			t.Parallel()
			runCtx := bindFixturePhase(t, row.RunCtx)
			profile := surface.ResolveTurnProfile(runCtx, row.Sess, coordinatorTurnHistory(row.History, row.UserPrompt), row.State)
			if profile.SurfaceID != row.SurfaceID {
				t.Fatalf("surface = %q want %q", profile.SurfaceID, row.SurfaceID)
			}
			if len(profile.ModeRefs) > 2 {
				t.Fatalf("len(modeRefs)=%d want ≤2", len(profile.ModeRefs))
			}
			if row.MaxModes > 0 && len(profile.ModeRefs) > row.MaxModes {
				t.Fatalf("modeRefs = %v", profile.ModeRefs)
			}
			if len(row.ModeRefs) > 0 && !sliceEqual(profile.ModeRefs, row.ModeRefs) {
				t.Fatalf("modeRefs = %v want %v", profile.ModeRefs, row.ModeRefs)
			}
		})
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
