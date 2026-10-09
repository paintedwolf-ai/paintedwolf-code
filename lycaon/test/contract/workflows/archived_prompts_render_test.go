package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// A resumed run on a sealed version renders that version's own guidance
// through the current engine. Sealed bytes are released content, so they are
// not held to size limits written after they shipped.
func TestArchivedGuidanceRendersFromItsArchive(t *testing.T) {
	t.Parallel()
	catalog, err := extpacks.ResolveStockCatalog(t.Context(), nil)
	contractcheck.FailErr(t, "resolve stock catalog", err)
	vars := map[string]any{
		"spawnable_reviewers":      true,
		"review_followup_attempts": 2,
		"claim_statuses":           []string{"survives", "refuted"},
		"rating_questions":         "Q1: Is there a bypass?",
		"review_verdict":           map[string]any{"survey_claims": map[string]any{"threat_model": "untrusted input"}},
	}

	var rendered int
	for _, unitID := range catalog.LoadedUnitIDs() {
		archiveKey, rest, ok := extpacks.SplitArchiveUnitID(unitID)
		stem, guidance := strings.CutPrefix(rest, "guidance/")
		if !ok || !guidance || strings.Contains(stem, "/") {
			continue
		}
		rendered++
		sealed, _, _ := catalog.UnitContent(unitID)
		reference := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Catalog: catalog})
		contractcheck.FailErr(t, "register sealed "+unitID, reference.Register("guidance/sealed-reference.md", string(sealed)))
		want, err := reference.Render(t.Context(), "guidance/sealed-reference.md", vars)
		contractcheck.FailErr(t, "render sealed bytes "+unitID, err)

		archived := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Catalog: catalog}).WithWorkflowArchive(archiveKey)
		got, err := archived.Render(t.Context(), "guidance/"+stem+".md", vars)
		contractcheck.FailErr(t, "render archived "+unitID, err)
		if got != want {
			t.Errorf("%s: the archive layer did not serve the sealed guidance", unitID)
		}
	}
	if rendered == 0 {
		t.Fatal("the stock catalog seals no guidance")
	}
}
