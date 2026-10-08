package contract

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestArchivedAndLivePromptsFitPromptBudgets asserts both the sealed 1.0.0 text
// and the live 2.0.0 text for every name-keyed prompt fit their prompt-budgets.yaml cap.
func TestArchivedAndLivePromptsFitPromptBudgets(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	archiveDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "security-survey", "archive", "1.0.0")

	budgets, err := prompts.LoadPromptBudgets()
	contractcheck.FailErr(t, "LoadPromptBudgets", err)

	liveEngine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	archiveEngine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		WorkflowArchive: archiveDir,
	})

	securityPrompts := []string{
		"coordinator-security-challenge",
		"coordinator-security-claims",
		"coordinator-security-execute",
		"coordinator-security-plan",
		"coordinator-security-synthesis",
	}

	sampleContext := map[string]any{
		"spawnable_reviewers":      true,
		"review_followup_attempts": 2,
		"claim_statuses":           []string{"survives", "refuted"},
		"rating_questions":         "Q1: Is there a bypass?",
		"review_verdict": map[string]any{
			"survey_claims": map[string]any{
				"threat_model": "untrusted input",
			},
		},
	}

	for _, promptID := range securityPrompts {
		budgetCap, ok := budgets.Kicks[promptID]
		if !ok {
			t.Fatalf("prompt %s has no budget cap under kicks in prompt-budgets.yaml", promptID)
		}

		t.Run(promptID+"_live", func(t *testing.T) {
			rendered, prov, err := liveEngine.RenderWithProvenance(context.Background(), "guidance/"+promptID+".md", sampleContext)
			if err != nil {
				t.Fatalf("live render error: %v", err)
			}
			if len(rendered) > budgetCap {
				t.Errorf("live %s rendered bytes %d exceeds cap %d", promptID, len(rendered), budgetCap)
			}
			if len(prov) == 0 {
				t.Errorf("live %s produced no provenance records", promptID)
			}
		})

		t.Run(promptID+"_sealed_1.0.0", func(t *testing.T) {
			rendered, prov, err := archiveEngine.RenderWithProvenance(context.Background(), "guidance/"+promptID+".md", sampleContext)
			if err != nil {
				t.Fatalf("sealed 1.0.0 render error: %v", err)
			}
			if len(rendered) > budgetCap {
				t.Errorf("sealed 1.0.0 %s rendered bytes %d exceeds cap %d", promptID, len(rendered), budgetCap)
			}
			foundArchiveTier := false
			for _, p := range prov {
				if p.SourceTier == "archive" {
					foundArchiveTier = true
					break
				}
			}
			if !foundArchiveTier {
				t.Errorf("sealed 1.0.0 %s did not resolve from archive tier: %+v", promptID, prov)
			}
		})
	}
}
