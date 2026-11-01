package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTurnRecipeSelectsAvailableSurveyRoutes(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, available := range []bool{false, true} {
		out, err := engine.Render(context.Background(), "partials/coordinator-turn-recipe.md", map[string]any{
			"profile_has_summarize": available,
			"profile_has_command":   available,
			"visual_show_available": available,
		})
		testutil.FailErr(t, "render turn recipe capability selection", err)
		for _, guidance := range []string{"start with `summarize(path=…)`", "find . -maxdepth 2 -type d"} {
			if strings.Contains(out, guidance) != available {
				t.Fatalf("available=%t: selection rule %q in %s", available, guidance, out)
			}
		}
	}
}

func TestTurnRecipeMapDoesNotRequireSummarize(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.Render(context.Background(), "partials/coordinator-turn-recipe.md", map[string]any{"profile_has_command": true})
	testutil.FailErr(t, "render map-only selection", err)
	if !strings.Contains(out, "find . -maxdepth 2 -type d") || strings.Contains(out, "summarize") {
		t.Fatalf("map-only surface has incorrect discovery routes: %s", out)
	}
}
