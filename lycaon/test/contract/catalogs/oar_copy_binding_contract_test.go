package contract

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// groundingRepairRecallCodes are the grounding and citation repairs that
// recover a recorded observation. Their copy must stand alone: the
// native-recall-tool instruction unit may be omitted from the turn.
var groundingRepairRecallCodes = []string{
	"AMBIENT_GROUNDING_ESCALATED",
	"AMBIENT_UNGROUNDED_COMPLETION",
	"FINDING_UNGROUNDED",
	"INVEST_CITATION_UNVERIFIABLE",
	"PAGE_MEASURE_UNGROUNDED",
	"SUBMIT_VERDICT_CITATION_UNGROUNDED",
	"SUBMIT_VERDICT_CITATIONS_REQUIRED",
	"SURFACE_CLAIM_UNGROUNDED",
	"SYNTH_CITATION_UNVERIFIABLE",
}

func TestGroundingRepairCopyDeclaresCanRecall(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load stock policy", err)
	for _, code := range groundingRepairRecallCodes {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Fatalf("%s missing from stock policy", code)
		}
		if !slices.Contains(entry.RequiresFacts, "paintedwolf.can_recall") {
			t.Errorf("%s does not require paintedwolf.can_recall", code)
		}
	}
}

// Repairs can only point at recall where every turn offers it.
func TestRecallStickyOnEveryProfileAndSurface(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	if len(profiles) == 0 {
		t.Fatal("no tool profiles loaded")
	}
	for _, prof := range profiles {
		if !prof.ToolAllowed("recall") || !prof.ToolSticky("recall") {
			t.Errorf("profile %s does not keep recall sticky", prof.ID)
		}
	}
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load surface catalog", err)
	for _, surfaceID := range catalog.SurfaceIDs() {
		plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, 1)
		contractcheck.FailErr(t, "CompileToolPlan "+surfaceID, err)
		if !slices.Contains(plan.ImmediateNames(), "recall") {
			t.Errorf("surface %s does not offer recall immediately", surfaceID)
		}
	}
}
