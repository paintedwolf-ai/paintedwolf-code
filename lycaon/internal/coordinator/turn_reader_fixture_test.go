package coordinator_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/turnreadertest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// withFixtureReader projects the fixture's declared requests for runtimes
// built without a prompt loop.
func withFixtureReader(deps assembly.AssemblyDeps) assembly.AssemblyDeps {
	return withListedFixtureReader(deps, nil)
}

// withListedFixtureReader projects requests offering what lister lists.
func withListedFixtureReader(deps assembly.AssemblyDeps, lister func(context.Context, *api.Session, string) ([]tools.ToolMeta, error)) assembly.AssemblyDeps {
	if deps.TurnReader != nil {
		return deps
	}
	f := turnreadertest.Fixture{
		Lister:         lister,
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
