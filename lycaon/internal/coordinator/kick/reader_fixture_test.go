package kick

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

// kickTestReader is the declared implement_investigate request on one root.
func kickTestReader(t *testing.T) prompts.Reader {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	reader, err := prompts.NewReader(prompts.HostFacts{Profiles: profiles, RootCount: 1, WebSearch: true},
		prompts.TurnProjection{ProfileID: prompts.CoordinatorProfileID, SurfaceID: "implement_investigate", Declared: true})
	testutil.FailErr(t, "reader", err)
	return reader
}
