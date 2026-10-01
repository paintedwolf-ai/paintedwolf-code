package prompts_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCoordinatorModesPreserveUserLimitsDuringDelegation(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, mode := range []string{"investigate", "orchestrate", "wrapup"} {
		for _, files := range []bool{false, true} {
			name := mode + "/rootless"
			if files {
				name = mode + "/project"
			}
			t.Run(name, func(t *testing.T) {
				out, err := engine.Render(t.Context(), "agents/coordinator-core.md", map[string]any{
					"execution_mode": mode, "has_file_tools": files, "can_spawn_web_research": true,
				})
				testutil.FailErr(t, "render coordinator delegation constraints", err)
				for _, required := range []string{
					"Preserve the user's scope and limits in every `brief.constraints`",
					"tools, workers, and workflow steps grant no exceptions",
					"Ask the user before conflicting actions",
				} {
					if !strings.Contains(out, required) {
						t.Errorf("rendered %s omits user-limit instruction %q", name, required)
					}
				}
			})
		}
	}
}

func TestSecurityChallengePreservesUserLimitsWithAvailableReviewers(t *testing.T) {
	renderer := prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	for _, reviewers := range []bool{false, true} {
		out, err := renderer.Render(t.Context(), "coordinator-security-challenge", map[string]any{
			"spawnable_reviewers": reviewers,
		})
		testutil.FailErr(t, "render security challenge constraints", err)
		for _, required := range []string{
			"Preserve user limits in `brief.constraints`",
			"If required review conflicts, `ask_user` before dispatch",
			"available reviewers grant no exception",
		} {
			if !strings.Contains(out, required) {
				t.Errorf("reviewers=%t omits user-limit instruction %q", reviewers, required)
			}
		}
	}
}
