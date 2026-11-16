package prompts_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
)

func TestRenderPersonaUnknownAgent(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	reader, err := prompts.DeclaredReader(nil, "implement")
	if err != nil {
		t.Fatalf("DeclaredReader: %v", err)
	}
	_, err = prompts.RenderPersona(context.Background(), engine, "not-a-worker", reader, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
