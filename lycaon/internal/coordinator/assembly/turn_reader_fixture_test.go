package assembly

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/turnreadertest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// toolLister lists the tools a fixture offers a profile.
type toolLister = func(ctx context.Context, sess *api.Session, profileID string) ([]tools.ToolMeta, error)

// withTestTurnReader binds the fixture's declared request projection.
func withTestTurnReader(deps AssemblyDeps) AssemblyDeps {
	return withListedTurnReader(deps, nil)
}

// withListedTurnReader binds a request projection offering what lister lists.
func withListedTurnReader(deps AssemblyDeps, lister toolLister) AssemblyDeps {
	if deps.TurnReader != nil {
		return deps
	}
	deps.TurnReader = fixture(deps, lister).TurnReader
	return deps
}

func fixture(deps AssemblyDeps, lister toolLister) turnreadertest.Fixture {
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
	if deps.CommandJobs != nil {
		f.CommandJobs = func(id string) bool { return len(deps.CommandJobs(id)) > 0 }
	}
	if deps.HeldCalls != nil {
		f.HeldCalls = func(id string) bool { return len(deps.HeldCalls(id)) > 0 }
	}
	return f
}

// declaredInvestigateReader serves the declared implement_investigate request.
func declaredInvestigateReader(t *testing.T) TurnReaderSource {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	reader, err := prompts.NewReader(prompts.HostFacts{Profiles: profiles, RootCount: 1, WebSearch: true},
		prompts.TurnProjection{ProfileID: prompts.CoordinatorProfileID, SurfaceID: "implement_investigate", Declared: true})
	testutil.FailErr(t, "declared reader", err)
	return func(context.Context, *api.Session, []api.Message, inject.CoordinatorTurnFrame) (prompts.Reader, error) {
		return reader, nil
	}
}
