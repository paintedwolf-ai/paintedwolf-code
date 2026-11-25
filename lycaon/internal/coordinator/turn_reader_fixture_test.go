package coordinator_test

import (
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/turnreadertest"
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
