package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHostRunnerPartialTeachesBoundaryBeforeActivation(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()
	out, err := engine.Render(ctx, "units/coordinator-host-runner.md", map[string]any{
		"profile_has_command":           true,
		"profile_has_terminal_open":     true,
		"profile_has_terminal_snapshot": true,
	})
	testutil.FailErr(t, "render host-runner partial", err)
	for _, leak := range []string{"{{", "}}", "{%", "%}", "<nil>"} {
		if strings.Contains(out, leak) {
			t.Fatalf("template rendering leak detected %q; output:\n%s", leak, out)
		}
	}
	for _, want := range []string{
		"Host runner", "confinement", "Egress is mediated",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in host-runner guidance; got %q", want, out)
		}
	}
	// The Deny follow-up rule lives once, in coordinator invariants — not here.
	if strings.Contains(out, "After a Deny") {
		t.Fatalf("host-runner must not duplicate the invariant Deny rule; got %q", out)
	}
	if strings.Contains(out, "→") {
		t.Fatalf("host-runner must not use workflow arrow between terminal tools; got %q", out)
	}
}

func TestTerminalNoSecretsUnitTeachesThePty(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()
	out, err := engine.Render(ctx, "units/terminal-no-secrets-over-pty.md", map[string]any{})
	testutil.FailErr(t, "render secrets unit", err)
	if !strings.Contains(out, "terminal_send") || !strings.Contains(out, "passwords") {
		t.Fatalf("expected secrets guidance; got %q", out)
	}
}
