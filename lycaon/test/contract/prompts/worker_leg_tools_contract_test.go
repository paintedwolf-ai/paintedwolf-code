package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestImplementerPersonaToolCheckGuard(t *testing.T) {
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := prompts.RenderPersona(context.Background(), engine, orchestration.ProfileImplementer, nil)
	contractcheck.FailErr(t, "RenderPersona", err)
	for _, sub := range []string{"SUBAGENT_MISCONFIGURED", "First-turn tool check", "write", "edit", "replace_lines", "web_search", "fetch_url"} {
		if !strings.Contains(got, sub) {
			t.Fatalf("implementer persona missing %q", sub)
		}
	}
}
