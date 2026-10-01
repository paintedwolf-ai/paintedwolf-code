package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenderPersonaImplementerStub(t *testing.T) {
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := prompts.RenderPersona(context.Background(), engine, "implementer", nil)
	testutil.FailErr(t, "prompts.RenderPersona failed", err)
	for _, heading := range []string{"## Focus", "## Process", "## Tools", "## Finish", "## Non-goals"} {
		if !strings.Contains(got, heading) {
			t.Fatalf("missing %s in %q", heading, got)
		}
	}
	if !strings.Contains(got, "DOOM_LOOP") {
		t.Fatalf("missing contract substrings in %q", got)
	}
	if !strings.Contains(got, "Tool schema") || !strings.Contains(got, "`path`") {
		t.Fatalf("missing agent tool surface in %q", got)
	}
	if strings.Contains(got, "command allowlist") {
		t.Fatalf("implementer must not surface command allowlist in %q", got)
	}
	if !strings.Contains(got, "Implementer") {
		t.Fatalf("missing role title in %q", got)
	}
}

// Validation guidance precedes closeout instructions.
func TestRenderPersonaImplementerKeepsValidationAdvisory(t *testing.T) {
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := prompts.RenderPersona(context.Background(), engine, "implementer", nil)
	testutil.FailErr(t, "prompts.RenderPersona failed", err)

	obligation := strings.Index(got, "complete_leg.verification")
	if obligation < 0 {
		t.Fatal("implementer persona omits verification reporting")
	}
	if !strings.Contains(got, "Validation is advisory for review and promotion") {
		t.Error("implementer persona must separate validation from delivery")
	}
	if closeout := strings.LastIndex(got, "complete_leg"); obligation > closeout {
		t.Errorf("verification guidance at %d sits after the last complete_leg mention at %d",
			obligation, closeout)
	}
}

func TestRenderPersonaAdvisoryIncludesPartial(t *testing.T) {
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := prompts.RenderPersona(context.Background(), engine, "plan-reviewer", nil)
	testutil.FailErr(t, "prompts.RenderPersona failed", err)
	if !strings.Contains(got, "advisory only") {
		t.Fatalf("missing advisory partial in %q", got)
	}
}

func TestRenderPersonaUnknownAgent(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	_, err := prompts.RenderPersona(context.Background(), engine, "not-a-worker", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
