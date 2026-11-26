package hoststate_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func TestZZDiagCustomizedBindings(t *testing.T) {
	states, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if !strings.Contains(st.ID, "/bind=fixture-") || st.PackOverlayID == "stock" {
			continue
		}
		auth, aErr := hoststate.FreezeTurnAuthority(t.Context(), st)
		offered := ""
		if aErr == nil && !st.Excluded {
			offered = strings.Join(auth.Offered, ",")
		}
		t.Logf("DIAG %s excluded=%t reason=%s surf=%s modes=%v tmpl=%s offered=%s err=%v", st.ID, st.Excluded, st.ExcludedReason, st.TurnProfile.SurfaceID, st.TurnProfile.ModeRefs, st.TurnProfile.SurfaceTemplate, offered, aErr)
	}
}
