package review

import (
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
)

func challengeWithSetAsides() workflowdef.ReviewLoopDef {
	_, challenge := surveyPhases()
	challenge.VerdictSchema = map[string]string{"verdict": "CHALLENGED", "challenges": "claims", "set_asides": "set_asides"}
	return challenge
}

func TestParseVerdictSetAsides(t *testing.T) {
	def := challengeWithSetAsides()
	got, err := workflowvalidation.ParseVerdictSetAsides(def, map[string]string{
		"set_asides": `[{"scanner":"lycaon-secrets","paths":["**/*_test.go"],"reason":"test fixtures"},{"scan_group_ids":["group:a"],"reason":"vendored"}]`,
	})
	testutil.FailErr(t, "parse set-asides", err)
	if len(got) != 2 || got[0].Scanner != "lycaon-secrets" || got[1].ScanGroupIDs[0] != "group:a" {
		t.Fatalf("set-asides = %+v", got)
	}
	for name, raw := range map[string]string{
		"missing reason":   `[{"scan_group_ids":["group:a"]}]`,
		"names no groups":  `[{"reason":"everything"}]`,
		"scanner no paths": `[{"scanner":"lycaon-sca","reason":"all of it"}]`,
		"unknown field":    `[{"reason":"r","scan_group_ids":["group:a"],"why":"x"}]`,
		"not an array":     `{"reason":"r"}`,
	} {
		if _, err := workflowvalidation.ParseVerdictSetAsides(def, map[string]string{"set_asides": raw}); err == nil || !strings.Contains(err.Error(), workflowvalidation.ReviewLoopVerdictInvalidCode) {
			t.Errorf("%s: err = %v, want %s", name, err, workflowvalidation.ReviewLoopVerdictInvalidCode)
		}
	}
	verdict := map[string]string{"verdict": "CHALLENGED", "challenges": `[]`, "set_asides": `[{"reason":"everything"}]`}
	if err := workflowvalidation.ValidateReviewLoopVerdict(def, verdict, workflowvalidation.VerdictRules{}); err == nil {
		t.Fatal("a verdict with an invalid set-aside validated")
	}
	verdict["set_asides"] = `[]`
	if err := workflowvalidation.ValidateReviewLoopVerdict(def, verdict, workflowvalidation.VerdictRules{}); err != nil {
		t.Fatalf("an empty set-aside list is a valid answer: %v", err)
	}
}

func TestVerdictScanGroupsIncludeSetAsideIDs(t *testing.T) {
	def := challengeWithSetAsides()
	got := VerdictScanGroups(def, map[string]string{
		"challenges": `[{"id":"c1","status":"refuted","statement":"s","scan_group_ids":["group:a"]}]`,
		"set_asides": `[{"scan_group_ids":["group:b"],"reason":"fixtures"}]`,
	})
	if strings.Join(got, ",") != "group:a,group:b" {
		t.Fatalf("groups = %v", got)
	}
}

func TestEmptySetAsidesNameSelectorsThatMatchNothing(t *testing.T) {
	inv := fakeInventory{run: []api.CodeScan{{
		ID: "s1", ScannerID: "lycaon-secrets", Status: api.CodeScanStatusComplete,
		Findings: []api.SecurityFinding{secretFinding("internal/x_test.go")},
	}}}
	empty, err := EmptySetAsides(t.Context(), inv, "run-1", []guidance.CoordinatorSetAside{
		{Scanner: "lycaon-secrets", Paths: []string{"**/*_test.go"}, Reason: "test fixtures"},
		{Scanner: "lycaon-sca", Paths: []string{"**"}, Reason: "lockfiles"},
	})
	testutil.FailErr(t, "empty set-asides", err)
	if len(empty) != 1 || empty[0] != "lockfiles" {
		t.Fatalf("empty = %v, want only the selector that matched nothing", empty)
	}
}

func TestRunSetAsidesAccountForGroupsInTheReportDocument(t *testing.T) {
	claimsDef, _ := surveyPhases()
	challenge := challengeWithSetAsides()
	verdicts := []workflowpresentation.PhaseVerdict{
		phaseVerdict("claims", claimsDef, "claims", `[{"id":"open","title":"Open","status":"claimed","statement":"s"}]`),
		phaseVerdict("challenge", challenge, "set_asides", `[{"scanner":"secrets","paths":["**/*_test.go"],"reason":"test fixtures"}]`),
	}
	sets := workflowpresentation.RunSetAsides(verdicts)
	if len(sets) != 1 || sets[0].Reason != "test fixtures" {
		t.Fatalf("run set-asides = %+v", sets)
	}
	facts := documentFacts(t)
	facts.SetAsides = sets
	facts.Inventory = RunInventory{Settled: true, Groups: []scanfindings.InventoryGroup{
		inventoryGroup("group:a", "sca", "go.mod"),
		inventoryGroup("group:b", "secrets", "internal/x_test.go"),
	}}
	if issue := firstIssue(validDocument(), facts); issue.Code != "" {
		t.Fatalf("issue = %+v, want the review's set-aside to account for the fixture group", issue)
	}
	doc := validDocument()
	doc.SetAsides = []guidance.CoordinatorSetAside{{Scanner: "sast", Paths: []string{"**"}, Reason: "unused"}}
	if issue := firstIssue(doc, facts); issue.Code != guidance.ReportDocumentInvalidCode || !strings.Contains(issue.Reason, "set_asides[0]") {
		t.Fatalf("issue = %+v, want the closeout's own empty set-aside named by its index", issue)
	}
}

func TestUnreportedClaims(t *testing.T) {
	claims := []workflowpresentation.RunClaim{
		{ID: "held", Class: workflowdef.ClaimHeld},
		{ID: "open", Class: workflowdef.ClaimOpen},
		{ID: "refuted", Class: workflowdef.ClaimFailed},
	}
	got := UnreportedClaims([]string{" refuted "}, claims)
	if len(got) != 1 || got[0].ID != "open" {
		t.Fatalf("unreported = %+v, want the open claim no finding carries", got)
	}
}
