package promptloop

import (
	"context"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPreparationProjectsCallableToolsFromTheLivePlan(t *testing.T) {
	live := false
	procedureProfile := ""
	loop := NewPromptLoopForTest(PromptLoopDeps{Context: ContextDeps{
		Policy:        registryTestPolicy{metas: []tools.ToolMeta{{Name: "command"}, {Name: "request_tools"}, {Name: "command_output", Deferred: true}}},
		LiveResources: func(string) toolcontract.ResourcePresence { return toolcontract.ResourcePresence{CommandJobs: live} },
		ContextSurfaceDeps: ContextSurfaceDeps{
			ToolProcedures: func(_ context.Context, _ *api.Session, profile string, _ []string) (string, error) {
				procedureProfile = profile
				return "Known tool procedures", nil
			},
		},
	}})
	sess := &api.Session{ID: "s1", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/project", Posture: api.SessionPostureBuild}
	frame := inject.CoordinatorTurnFrame{Machine: inject.Machine{ProfileID: prompts.CoordinatorProfileID}}
	for _, active := range []bool{false, true} {
		live = active
		request, err := loop.ProjectPreparation(t.Context(), sess, nil, []api.Message{{Role: api.MessageRoleSystem, Content: "Standing", PromptCacheBreakpoint: api.PromptCacheTierStanding}}, frame)
		projected := request.Tools
		testutil.FailErr(t, "project callable schemas", err)
		if procedureProfile != "implement_investigate" {
			t.Fatalf("procedure profile = %q, want resolved coordinator surface", procedureProfile)
		}
		if len(request.Messages) != 2 || request.Messages[0].PromptCacheBreakpoint != api.PromptCacheTierNone || request.Messages[1].Content != "Known tool procedures" || request.Messages[1].PromptCacheBreakpoint != api.PromptCacheTierStanding {
			t.Fatal("preparation omitted the callable tools' standing procedures")
		}
		actual, _ := mustCoordinatorToolsForTurn(t, loop, sess, frame)
		if !reflect.DeepEqual(projected, actual) || containsMeta(projected, "command_output") != active {
			t.Fatalf("projected schemas differ from live plan: projected=%v actual=%v active=%v", namesOf(projected), namesOf(actual), active)
		}
	}
}
