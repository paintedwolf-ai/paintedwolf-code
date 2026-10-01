package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
)

func TestGuidanceRenderer_RejectsPathEscape(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderer := prompts.NewGuidanceRenderer(engine)
	_, err := renderer.Render(context.Background(), "../inject/active-workflow", nil)
	if err == nil || !strings.Contains(err.Error(), "invalid guidance ref") {
		t.Fatalf("err = %v", err)
	}
}
