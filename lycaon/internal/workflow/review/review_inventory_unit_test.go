package review

import (
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"testing"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
)

func TestReviewInventoryUsesInheritedLinksAndSetAsides(t *testing.T) {
	claims, challenge := surveyPhases()
	claims.VerdictSchema["set_asides"] = "set_asides"
	challenge.VerdictSchema["set_asides"] = "set_asides"
	prior := phaseVerdict("claims", claims, "claims", `[{"id":"c1","title":"Assessed","statement":"s","status":"claimed","scan_group_ids":["group:linked"]}]`)
	prior.Record.Artifacts["set_asides"] = `[{"scanner":"secrets","paths":["**/*_test.go"],"reason":"fixtures"}]`
	current := phaseVerdict("challenge", challenge, "challenges", `[{"id":"c1","statement":"s","status":"survives"}]`)
	current.Record.Artifacts["set_asides"] = `[{"scan_group_ids":["group:mixed","group:locationless"],"reason":"reviewed samples"}]`
	phases := []workflowpresentation.PhaseVerdict{prior, current}
	facts := ReportDocumentFacts{
		Claims: workflowpresentation.ReconcileClaims(phases), SetAsides: workflowpresentation.RunSetAsides(phases),
		Inventory: RunInventory{Settled: true, Groups: []scanfindings.InventoryGroup{
			{ID: "group:linked", Paths: []string{"src/a.go"}},
			{ID: "group:fixture", Scanner: "secrets", Paths: []string{"src/a_test.go"}},
			{ID: "group:mixed", Scanner: "secrets", Paths: []string{"src/a_test.go", "src/sample.go"}},
			{ID: "group:locationless", Scanner: "secrets"},
		}},
	}
	if issue := checkInventoryAccounted(validDocument(), facts); issue.Code != "" {
		t.Fatalf("inherited accounting was lost: %+v", issue)
	}
	facts.SetAsides = workflowpresentation.RunSetAsides(phases[:1])
	if issue := checkInventoryAccounted(validDocument(), facts); issue.Count != 2 {
		t.Fatalf("path globs cleared mixed or locationless groups: %+v", issue)
	}
}
