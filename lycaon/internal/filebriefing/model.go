package filebriefing

import (
	"context"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
)

type GenerationRequest struct {
	ProjectID    string
	ProjectDir   string
	Trigger      string
	SystemPrompt string
	Prompt       string
	MaxTokens    int
}

type Generator interface {
	Generate(context.Context, GenerationRequest, func(string)) (string, error)
}

type modelGenerator struct {
	service *llm.Service
	cost    cost.CostTracker
}

// NewModelGenerator returns nil when provider utility calls are unavailable.
func NewModelGenerator(service *llm.Service, tracker cost.CostTracker) Generator {
	if !llm.ProviderUtilityCallsEnabled() || service == nil || service.Registry == nil || service.Policy == nil {
		return nil
	}
	return &modelGenerator{service: service, cost: tracker}
}

func (g *modelGenerator) Generate(ctx context.Context, request GenerationRequest, onDelta func(string)) (string, error) {
	class := llm.UtilityClassRequested
	if request.Trigger == "automatic" {
		class = llm.UtilityClassOverlay
	}
	generator := g.service.BindSummarizer(&llm.RegistrySummarizer{
		Scope: llm.SettingsScopeProject, ProjectID: request.ProjectID, ProjectDir: request.ProjectDir,
		Cost: g.cost, Purpose: "file_briefing", Class: class,
	})
	return generator.SummarizeStreamOnce(ctx, request.SystemPrompt, request.Prompt, request.MaxTokens, onDelta)
}
