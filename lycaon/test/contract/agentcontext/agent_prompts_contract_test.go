package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBundledAgentTemplatesExist(t *testing.T) {
	t.Parallel()
	reg := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), reg))
	if err := orchestration.ValidateGateAgents(reg); err != nil {
		contractcheck.FailErr(t, "validate gate agent references in bundled registry", err)
	}

	engine := contractPersonaEngine(t)
	contract, _ := prompts.LoadPersonaContract()
	for _, profile := range reg.List() {
		ref := profile.SystemPromptTemplate
		if ref == "" {
			continue
		}
		if contract != nil && contract.IsWorkerAgent(profile.ID) {
			if _, err := prompts.RenderPersona(context.Background(), engine, profile.ID, nil); err != nil {
				t.Fatalf("agent %q persona render: %v", profile.ID, err)
			}
			continue
		}
		if _, err := engine.Render(context.Background(), ref, nil); err != nil {
			t.Fatalf("agent %q template %q: %v", profile.ID, ref, err)
		}
	}
}
