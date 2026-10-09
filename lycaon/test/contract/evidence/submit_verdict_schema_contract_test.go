package contract

import (
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"testing"
)

func TestSubmitVerdictSchemaRequiresEveryPhaseDiscriminant(t *testing.T) {
	cfg, _, err := extpacks.LoadEffectiveToolSchemas(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "load verdict schema", err)
	meta, ok := cfg.ToolMeta("submit_verdict")
	if !ok {
		t.Fatal("submit_verdict schema missing")
	}
	full := tools.ToolMeta{Name: "submit_verdict", Description: meta.Description, ArgsSchema: meta.ArgsSchema}
	registry, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "load workflow phases", err)
	phases := 0
	for key, manifest := range registry.All() {
		for _, phase := range manifest.PhaseDefs {
			if phase.ReviewLoop == nil {
				continue
			}
			phases++
			t.Run(key+"/"+phase.ID, func(t *testing.T) {
				if workflowvalidation.ValidateReviewLoopVerdict(*phase.ReviewLoop, map[string]string{}, workflowvalidation.VerdictRules{}) == nil {
					t.Fatal("runtime no longer requires a verdict discriminant")
				}
				for _, projected := range []tools.ToolMeta{full, tools.TrimCoordinatorToolMeta(full)} {
					for _, invalid := range []map[string]any{{}, {"verdict": ""}, {"verdict": nil}, {"verdict": 7}, {"claims": []any{}}} {
						if tools.ValidateToolArgs(projected.ArgsSchema, map[string]any{"verdict": invalid}) == nil {
							t.Fatalf("provider schema accepted missing/invalid discriminant: %v", invalid)
						}
					}
					for _, value := range phase.ReviewLoop.Decisions() {
						args := map[string]any{"verdict": map[string]any{
							"verdict": value, "claims": []any{}, "threat_model": "fixture",
						}}
						contractcheck.FailErr(t, "allow phase-specific verdict fields", tools.ValidateToolArgs(projected.ArgsSchema, args))
					}
				}
			})
		}
	}
	if phases == 0 {
		t.Fatal("no review phases exercised")
	}
}
