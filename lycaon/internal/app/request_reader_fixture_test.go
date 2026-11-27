package app

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

// declaredRequestReader is the declared implement_investigate request on one root.
func declaredRequestReader(t *testing.T) prompts.Reader {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	reader, err := prompts.NewReader(prompts.HostFacts{Profiles: profiles, RootCount: 1, WebSearch: true},
		prompts.TurnProjection{ProfileID: prompts.CoordinatorProfileID, SurfaceID: "implement_investigate", Declared: true})
	testutil.FailErr(t, "declared reader", err)
	return reader
}

// declaredWorkerRequests projects a worker agent's declared request.
type declaredWorkerRequests struct{}

func (r declaredWorkerRequests) WorkerRequestReader(_ context.Context, _, _, agentType string) (prompts.Reader, error) {
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return prompts.Reader{}, err
	}
	profileID, err := prompts.ToolProfileForAgent(agentType)
	if err != nil {
		return prompts.Reader{}, err
	}
	return prompts.NewReader(prompts.HostFacts{Profiles: profiles, RootCount: 1, WebSearch: true},
		prompts.TurnProjection{ProfileID: profileID, Declared: true})
}
