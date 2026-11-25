package coordinator_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/turnreadertest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

// withFixtureReader projects the fixture's requests for runtimes built
// without a prompt loop.
func withFixtureReader(deps assembly.AssemblyDeps) assembly.AssemblyDeps {
	if deps.TurnReader != nil {
		return deps
	}
	f := turnreadertest.Fixture{
		Lister:         deps.PromptToolLister,
		View:           deps.SessionView,
		WebSearch:      deps.WebSearchEnabled,
		Roots:          deps.WorkspaceRoots,
		ImplementState: deps.ImplementSessionState,
	}
	if deps.Selection != nil {
		f.Loaded = deps.Selection.LoadedTools
	}
	deps.TurnReader = f.TurnReader
	return deps
}

// declaredKickReader is the declared implement_investigate request on one root.
func declaredKickReader(t *testing.T) prompts.Reader {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	reader, err := prompts.NewReader(prompts.HostFacts{Profiles: profiles, RootCount: 1, WebSearch: true},
		prompts.TurnProjection{ProfileID: prompts.CoordinatorProfileID, SurfaceID: "implement_investigate", Declared: true})
	testutil.FailErr(t, "declared reader", err)
	return reader
}
