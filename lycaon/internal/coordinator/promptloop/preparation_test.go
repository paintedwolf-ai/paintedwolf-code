package promptloop

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPreparationProjectsCallableToolsFromTheLivePlan(t *testing.T) {
	loaded := map[string]bool{}
	loop := NewPromptLoopForTest(PromptLoopDeps{Context: ContextDeps{
		Policy: registryTestPolicy{metas: []tools.ToolMeta{{Name: "read"}, {Name: "request_tools"}, {Name: "command", Deferred: true}}},
		LoadedTools: func(string) map[string]bool { return loaded },
	}})
	sess := &api.Session{ID: "s1", AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/project", Posture: api.SessionPostureBuild}
	frame := inject.CoordinatorTurnFrame{Machine: inject.Machine{ProfileID: prompts.CoordinatorProfileID}}
	for _, active := range []bool{false, true} {
		if active {
			loaded["command"] = true
		}
		projected, err := loop.ProjectTools(t.Context(), sess, nil, frame)
		testutil.FailErr(t, "project callable schemas", err)
		actual, _ := mustCoordinatorToolsForTurn(t, loop, sess, frame)
		if !reflect.DeepEqual(projected, actual) || containsMeta(projected, "command") != active {
			t.Fatalf("projected schemas differ from live plan: projected=%v actual=%v active=%v", namesOf(projected), namesOf(actual), active)
		}
	}
}
