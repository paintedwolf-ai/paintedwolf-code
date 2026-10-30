package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPolicyBindingUsesTheFinalFittedRequest(t *testing.T) {
	var delivered []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Policy: registryTestPolicy{metas: []tools.ToolMeta{{Name: "command"}}},
			BuildMessages: func(_ context.Context, _ *api.Session, rows []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return append([]api.Message{{ID: "candidate-policy", Role: api.MessageRoleSystem, Content: "candidate"}}, rows...), nil
			},
			BindAgentPolicy: func(_ context.Context, _ *api.Session, messages []api.Message) (tools.AgentPolicyCheck, error) {
				delivered = append([]api.Message(nil), messages...)
				return func(context.Context, []agentpolicy.Target) error { return nil }, nil
			},
		},
		Model: ModelDeps{FitRequest: func(_ context.Context, _ *api.Session, _ []api.Message, request modelcall.CompletionRequest) (modelcall.CompletionRequest, error) {
			request.Messages = []api.Message{{ID: "fitted-policy", Role: api.MessageRoleSystem, Content: "fitted"}}
			return request, nil
		}},
	})
	sess := &api.Session{ID: "policy-request", AgentType: prompts.CoordinatorProfileID, Posture: api.SessionPostureBuild}
	state := &promptLoopTurnState{}
	request, err := loop.Model.buildTurnRequest(t.Context(), sess, sess.ID, []api.Message{{Role: api.MessageRoleUser, Content: "inspect"}}, prompts.CoordinatorProfileID, "inspect", 0, 20, false, state)
	testutil.FailErr(t, "build policy request", err)
	if len(delivered) != 1 || delivered[0].ID != "fitted-policy" || delivered[0].Content != request.Req.Messages[0].Content || state.output.policyCheck == nil {
		t.Fatalf("delivered policy = %+v", delivered)
	}
}
