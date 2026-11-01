package promptloop

import (
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
	loop := NewPromptLoopForTest(PromptLoopDeps{Context: ContextDeps{
		Policy:        registryTestPolicy{metas: []tools.ToolMeta{{Name: "command"}, {Name: "request_tools"}, {Name: "command_output", Deferred: true}}},
		LiveResources: func(string) toolcontract.ResourcePresence { return toolcontract.ResourcePresence{CommandJobs: live} },
	}})
	sess := &api.Session{ID: "s1", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/project", Posture: api.SessionPostureBuild}
	frame := inject.CoordinatorTurnFrame{Machine: inject.Machine{ProfileID: prompts.CoordinatorProfileID}}
	for _, active := range []bool{false, true} {
		live = active
		projected, err := loop.ProjectTools(t.Context(), sess, nil, frame)
		testutil.FailErr(t, "project callable schemas", err)
		actual, _ := mustCoordinatorToolsForTurn(t, loop, sess, frame)
		if !reflect.DeepEqual(projected, actual) || containsMeta(projected, "command_output") != active {
			t.Fatalf("projected schemas differ from live plan: projected=%v actual=%v active=%v", namesOf(projected), namesOf(actual), active)
		}
	}
}
