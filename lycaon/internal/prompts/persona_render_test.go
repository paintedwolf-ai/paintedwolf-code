package prompts_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
)

func TestRenderPersonaUnknownAgent(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	_, err := prompts.RenderPersona(context.Background(), engine, "not-a-worker", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
