package policyfacts

import (
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionOccurrencesPreserveOfferedRecoverySnapshot(t *testing.T) {
	manager := New(nil)
	for _, name := range []string{"ask_user", "secret_generate", "terminal_read", "command_stop"} {
		for _, offered := range []bool{false, true} {
			names := []string{}
			if offered {
				names = append(names, name)
			}
			ctx := tools.WithRecoveryTools(t.Context(), names)
			gc := oar.NewGuardContext()
			manager.FillSessionFacts(ctx, gc, &api.Session{ID: "fixture"}, "read", nil)
			if len(gc.ObservationData) != 0 {
				t.Fatal("recovery observations assembled before reference")
			}
			testutil.FailErr(t, "publish recovery fact", gc.Ensure("paintedwolf.can_"+name))
			if gc.ObservationData["can_"+name] != offered {
				t.Fatalf("recovery %s did not preserve offered=%v", name, offered)
			}
		}
	}
}
