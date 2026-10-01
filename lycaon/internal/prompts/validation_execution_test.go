package prompts_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidationExecutionMatchesOfferedTools(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, template := range []string{"partials/coordinator-verify-before-close.md", "partials/worker-write-leg-verify.md"} {
		for _, selected := range []bool{false, true} {
			for _, verify := range []bool{false, true} {
				for _, command := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/selected=%v/verify=%v/command=%v", template, selected, verify, command), func(t *testing.T) {
						vars := map[string]any{"profile_has_verify": verify, "profile_has_command": command}
						if selected {
							vars["verify_command"] = "project-check"
						}
						got, err := engine.Render(t.Context(), template, vars)
						testutil.FailErr(t, "render validation guidance", err)
						if !verify && strings.Contains(got, "verify(") {
							t.Fatalf("unavailable verify recipe: %s", got)
						}
						if !command && strings.Contains(got, "command(") {
							t.Fatalf("unavailable command recipe: %s", got)
						}
						if selected && verify && !strings.Contains(got, "Bare `verify()` runs it") {
							t.Fatal("selected check not runnable")
						}
						if selected && !verify && command && !strings.Contains(got, "command(command: \"project-check\"") {
							t.Fatal("command-only selected check not runnable")
						}
						if !selected && verify && !strings.Contains(got, "verify(command:") {
							t.Fatal("unselected verify has no command recipe")
						}
						if !selected && command && !strings.Contains(got, "command(verification: true)") {
							t.Fatal("command-only check has no evidence recipe")
						}
						if !verify && !command && !strings.Contains(got, "No execution tool is offered") {
							t.Fatal("missing inspection fallback")
						}
					})
				}
			}
		}
	}
}
