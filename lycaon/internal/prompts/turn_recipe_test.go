package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTurnRecipeSelectsAvailableSurveyAndCaptureRoutes(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, available := range []bool{false, true} {
		out, err := engine.Render(context.Background(), "partials/coordinator-turn-recipe.md", map[string]any{
			"profile_has_summarize": available,
			"profile_has_list_dir":  available,
			"visual_show_available": available,
		})
		testutil.FailErr(t, "render turn recipe capability selection", err)
		for _, guidance := range []string{"start with `summarize(path=…)`", "returns the map; omit depth and pagination fields", "include a separate Snapshot row"} {
			if strings.Contains(out, guidance) != available {
				t.Fatalf("available=%t: selection rule %q in %s", available, guidance, out)
			}
		}
	}
}

func TestTurnRecipeMapDoesNotRequireSummarize(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.Render(context.Background(), "partials/coordinator-turn-recipe.md", map[string]any{"profile_has_list_dir": true})
	testutil.FailErr(t, "render map-only selection", err)
	if !strings.Contains(out, "returns the map") || strings.Contains(out, "summarize") {
		t.Fatalf("map-only surface has incorrect discovery routes: %s", out)
	}
}
