package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCoordinatorTurnRecipeLoadsSkillsForSpecializedProcedures(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.Render(context.Background(), "partials/coordinator-turn-recipe.md", map[string]any{
		"profile_has_skills_read": true,
	})
	testutil.FailErr(t, "render coordinator turn recipe", err)
	for _, phrase := range []string{"specialized procedure", "`skills_read`", "Routine tool calls do not require a skill"} {
		if !strings.Contains(out, phrase) {
			t.Fatalf("skill selection guidance missing %q: %q", phrase, out)
		}
	}
	if strings.Contains(out, "One `command`") {
		t.Fatalf("turn recipe must not duplicate command batching guidance: %q", out)
	}
}
