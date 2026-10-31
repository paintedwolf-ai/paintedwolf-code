package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTerminalNoSecretsUnitTeachesThePty(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()
	out, err := engine.Render(ctx, "units/terminal-no-secrets-over-pty.md", map[string]any{})
	testutil.FailErr(t, "render secrets unit", err)
	if !strings.Contains(out, "terminal_send") || !strings.Contains(out, "passwords") {
		t.Fatalf("expected secrets guidance; got %q", out)
	}
}
