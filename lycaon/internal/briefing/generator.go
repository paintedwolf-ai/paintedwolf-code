package briefing

import (
	"context"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
)

// MockText stands in for model output under LYCAON_LLM_MOCK.
const MockText = "Advances the current task toward the stated goal."

type modelGenerator struct {
	service *llm.Service
	cost    cost.CostTracker
}

// NewModelGenerator returns nil when provider utility calls are unavailable.
func NewModelGenerator(service *llm.Service, tracker cost.CostTracker) Generator {
	if llm.MockOnlyFromEnv() {
		return mockGenerator{}
	}
	if !llm.ProviderUtilityCallsEnabled() || service == nil || service.Registry == nil || service.Policy == nil {
		return nil
	}
	return &modelGenerator{service: service, cost: tracker}
}

func (g *modelGenerator) Generate(ctx context.Context, invocation Invocation) (string, string, string, error) {
	class := llm.UtilityClassOverlay
	if invocation.Requested {
		class = llm.UtilityClassRequested
	}
	summarizer := &llm.RegistrySummarizer{Fallback: compaction.UnavailableSummarizer{}, Cost: g.cost, Purpose: invocation.Purpose, Class: class}
	if invocation.ProjectID != "" {
		summarizer.Scope, summarizer.ProjectID, summarizer.ProjectDir = llm.SettingsScopeProject, invocation.ProjectID, invocation.ProjectDir
	}
	text, err := g.service.BindSummarizer(summarizer).Summarize(ctx, invocation.Prompt.System, invocation.Prompt.User, invocation.Prompt.MaxTokens)
	return text, "", "", err
}

type mockGenerator struct{}

func (mockGenerator) Generate(context.Context, Invocation) (string, string, string, error) {
	return MockText, "mock", "mock", nil
}
