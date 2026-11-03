package prompts_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandInputGuidanceRequiresBothInteractiveTools(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, open := range []bool{false, true} {
		for _, send := range []bool{false, true} {
			out, err := engine.Render(t.Context(), "partials/command-surface.md", map[string]any{"profile_has_terminal_open": open, "profile_has_terminal_send": send})
			testutil.FailErr(t, "render command input guidance", err)
			if !strings.Contains(out, "Stdin is empty") || strings.Contains(out, "terminal_open") != (open && send) || strings.Contains(out, "terminal_send") != (open && send) {
				t.Fatalf("unreachable input recovery: open=%t send=%t\n%s", open, send, out)
			}
		}
	}
}
