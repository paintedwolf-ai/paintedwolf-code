package contract

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// recallTool is the native tool grounding repairs point at to recover a
// recorded observation.
const recallTool = "recall"

func stepsCall(steps []oar.NextStep, tool string) bool {
	for _, step := range steps {
		if step.Tool == tool || stepsCall(step.Or, tool) {
			return true
		}
	}
	return false
}

// TestGroundingRepairCopyDeclaresCanRecall requires every stock rule whose next
// steps call recall to declare the can_recall fact, so the frozen request
// answers whether the call is reachable on the turn.
func TestGroundingRepairCopyDeclaresCanRecall(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	contractcheck.FailErr(t, "install catalog", anchorcatalog.InstallFile(filepath.Join(root,
		"lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	contractcheck.FailErr(t, "oar loader", err)
	rules, err := loader.LoadEffectivePolicyWithCatalog(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "load stock policy", err)
	canRecall := oarcopy.HostFactNamespace + ".can_" + recallTool
	var callers int
	for _, rule := range rules.All() {
		if rule.NextSteps == nil || !stepsCall(rule.NextSteps.Steps, recallTool) {
			continue
		}
		callers++
		if !slices.Contains(rule.RequiresFacts, canRecall) {
			t.Errorf("%s calls %s in its next steps but does not require %s", rule.ID, recallTool, canRecall)
		}
	}
	if callers == 0 {
		t.Fatalf("no stock rule calls %s in its next steps", recallTool)
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
