package promptloop

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionRoutingClientHonorsWorkerPool(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "0")
	t.Setenv("LYCAON_LLM_MANUAL", "0")
	policy := llm.NewInMemoryPolicyStore(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "coordinator-provider", Model: "coordinator-model"},
		AgentPool: llm.AgentPool{Selection: llm.PoolSelectionFirst,
			Models: []llm.ModelRef{{ProviderID: "worker-provider", Model: "worker-model"}}},
	})
	loop := &PromptLoop{Deps: PromptLoopDeps{LLMService: &llm.Service{
		Registry: &llm.Registry{}, Router: llm.NewStaticModelRouter(policy),
	}}}
	for _, tc := range []struct {
		name     string
		session  api.Session
		provider string
	}{
		{"coordinator", api.Session{}, "coordinator-provider"},
		{"worker", api.Session{ParentSessionID: "parent"}, "worker-provider"},
		{"override", api.Session{ParentSessionID: "parent", ProviderID: "override-provider", Model: "override-model"}, "override-provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := modelTurn{loop}.sessionRoutingClient(&tc.session).Stream(t.Context(), modelcall.CompletionRequest{})
			var missing *failure.ProviderNotConfiguredError
			if !errors.As(err, &missing) || missing.ProviderID != tc.provider {
				t.Fatalf("selected provider error = %v, want %s", err, tc.provider)
			}
		})
	}
}
