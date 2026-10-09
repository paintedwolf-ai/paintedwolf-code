package assembly

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestAgentsMDIndexRemainsPinnedAcrossRequests(t *testing.T) {
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{AgentsMDIndex: func(context.Context, *api.Session) (api.Message, bool) {
		return api.Message{Role: api.MessageRoleSystem, Content: "session index"}, true
	}})
	turn := &TurnAssemblyScratch{}
	for range 3 {
		messages := testTurnContext(engine).agentsMDIndexInject(t.Context(), &api.Session{ID: "session"}, turn)
		if len(messages) != 1 || messages[0].Content != "session index" || !messages[0].ContextPinned {
			t.Fatalf("standing index disappeared or became trimmable: %+v", messages)
		}
	}
}
