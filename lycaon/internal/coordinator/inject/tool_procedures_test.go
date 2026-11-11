package inject_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCalibratedPruningPreservesCoordinatorRequirements(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	catalog, err := renderer.UnitCatalog()
	testutil.FailErr(t, "load instruction catalog", err)
	policy, err := turnload.LoadCatalog()
	testutil.FailErr(t, "load decision release policy", err)
	guides := turnload.GuideCandidates(catalog.Units())
	scores := make(map[string]float64, len(guides))
	for _, guide := range guides {
		scores[guide.ID] = 0
	}
	engine := &decidetest.Fake{Answers: decide.Answers{
		"guides": {Kind: decide.KindMulti, Probabilities: scores, Confidence: 1},
	}}
	decision := turnload.Decide(t.Context(), engine, policy.Turn, turnload.State{User: "explain the result"}, guides)
	if decision.Abstained || len(decision.Omitted) == 0 {
		t.Fatalf("pruning did not run: %+v", decision)
	}
	omitted := make(map[string]bool, len(decision.Omitted))
	for id := range decision.Omitted {
		omitted[id] = true
	}
	selection := promptunit.Selection{Host: promptunit.HostCoordinator,
		Offered: map[string]bool{"edit": true, "update_progress": true}, Omitted: omitted}
	rendered := catalog.Rendered(selection)
	for _, id := range []string{"coordinator-progress-procedure", "visual-show-the-design"} {
		found := false
		for _, units := range rendered {
			for _, unit := range units {
				found = found || unit.ID == id
			}
		}
		if !found {
			t.Fatalf("calibrated pruning removed required unit %q", id)
		}
	}
	block, err := inject.RenderToolProceduresBlock(
		t.Context(), renderer, "session", prompts.CoordinatorProfileID,
		[]string{"command", "update_progress"}, omitted,
	)
	testutil.FailErr(t, "render tool procedures block", err)
	if !strings.Contains(block, "Snapshot the running UI") || !strings.Contains(block, "Snapshot the terminal") {
		t.Fatalf("rendered tool procedures block missing visual snapshot checklist cue: %q", block)
	}
}
