package oar

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEvalSelectionUsesDeclaredPredicatesDespiteObservedRejection(t *testing.T) {
	ensureCatalog(t)
	loader, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "load schema", err)
	rules := NewRuleSet([]*Rule{
		{ID: "A_UNRELATED_FAILURE", Kind: KindPolicy, Anchor: AnchorToolHandler, Effect: EffectBlock, Enforcement: "enforce", When: "policy_denied"},
		{ID: "Z_ORIGINAL_FAILURE", Kind: KindPolicy, Anchor: AnchorToolHandler, Effect: EffectBlock, Enforcement: "enforce", When: "policy_denied"},
	})
	pipeline := NewGuardPipeline(rules, loader, NewCounterStore())
	pipeline.EnableAnchor(AnchorToolHandler)
	for _, mode := range []string{"anchor", "stage"} {
		for _, code := range []string{"Z_ORIGINAL_FAILURE", "FUTURE_TOOL_FAILURE", ""} {
			t.Run(mode+"/"+code, func(t *testing.T) {
				gc := NewGuardContext()
				gc.Tool = "future_tool"
				gc.PolicyDenied = true
				gc.ObservedRejectCode = code
				var result *PipelineResult
				var err error
				if mode == "anchor" {
					result, err = pipeline.EvaluateBlock(t.Context(), AnchorToolHandler, gc)
				} else {
					result, err = pipeline.Evaluate(t.Context(), StageFromAnchor(AnchorToolHandler), gc)
				}
				testutil.FailErr(t, "evaluate rejection", err)
				want := "A_UNRELATED_FAILURE"
				if result.Decision == nil || result.Decision.Code != want {
					t.Fatalf("decision = %+v, want %s", result.Decision, want)
				}
			})
		}
	}
}

func TestFact21RejectionMetadataCannotReplaceOccurrenceIdentity(t *testing.T) {
	gc := NewGuardContext()
	gc.ObserveToolCall("mcp_provider_action", nil)
	gc.Principal = "person"
	gc.PermissionProfile = "restricted"
	gc.ObservedRejectCode = "ORIGINAL_FAILURE"
	gc.PutRejectData("ORIGINAL_FAILURE", map[string]any{
		"tool": "spoofed", "principal": "spoofed", "permission_profile": "spoofed", "rejection_code": "spoofed",
	})
	facts := activation(gc)
	for name, want := range map[string]string{
		"tool": "mcp_provider_action", "principal": "person", "permission_profile": "restricted", "paintedwolf.rejection_code": "ORIGINAL_FAILURE",
	} {
		if facts[name] != want {
			t.Errorf("[OAR-FACT-21] metadata replaced %s: got %v, want %s", name, facts[name], want)
		}
	}
}
