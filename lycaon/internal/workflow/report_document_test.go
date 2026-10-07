package workflow

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func surveyPhases() (workflowdef.ReviewLoopDef, workflowdef.ReviewLoopDef) {
	claims := workflowdef.ReviewLoopDef{
		EvidenceKey:   "survey_claims",
		VerdictSchema: map[string]string{"verdict": "CLAIMED", "claims": "claims"},
		ClaimStatuses: map[string]workflowdef.ClaimClass{"claimed": workflowdef.ClaimOpen},
	}
	challenge := workflowdef.ReviewLoopDef{
		EvidenceKey:     "survey_challenged",
		ReconcilesPhase: "claims",
		VerdictSchema:   map[string]string{"verdict": "CHALLENGED", "challenges": "claims"},
		ClaimStatuses:   map[string]workflowdef.ClaimClass{"survives": workflowdef.ClaimHeld, "refuted": workflowdef.ClaimFailed, "unresolved": workflowdef.ClaimOpen},
		BriefLabel:      "Second opinion",
	}
	return claims, challenge
}

func phaseVerdict(phase string, def workflowdef.ReviewLoopDef, member, claims string) PhaseVerdict {
	return PhaseVerdict{Phase: phase, Def: def, Record: evidence.Record{Artifacts: map[string]any{
		"verdict": strings.Split(def.VerdictSchema["verdict"], "|")[0], member: claims,
	}}}
}

// The phase that introduces a claim titles it, each later phase that restates
// it settles its class, and a claim the reconciling phase leaves out stays open.
func TestReconcileClaims_OriginTitlesLaterPhasesSettle(t *testing.T) {
	claimsDef, challengeDef := surveyPhases()
	got := ReconcileClaims([]PhaseVerdict{
		phaseVerdict("claims", claimsDef, "claims", `[
			{"id":"a","title":"API fails closed","status":"claimed","statement":"s","answers":{"reachable":"not_reachable"}},
			{"id":"b","title":"Parser overflows","status":"claimed","statement":"s","scan_group_ids":["group:1"]},
			{"id":"c","title":"Dropped claim","status":"claimed","statement":"s"}]`),
		phaseVerdict("challenge", challengeDef, "challenges", `[
			{"id":"a","status":"survives","statement":"holds"},
			{"id":"b","status":"refuted","statement":"no overflow","answers":{"reachable":"reachable"}},
			{"id":"d","title":"New advisory","status":"unresolved","statement":"open"}]`),
	})
	byID := map[string]RunClaim{}
	for _, c := range got {
		byID[c.ID] = c
	}
	if len(got) != 4 {
		t.Fatalf("claims = %+v, want four", got)
	}
	if a := byID["a"]; a.Title != "API fails closed" || a.Class != workflowdef.ClaimHeld || a.Phase != "challenge" || a.Origin != "claims" || a.Answers["reachable"] != "not_reachable" {
		t.Fatalf("a = %+v, want the origin title and answers with the challenge's class", a)
	}
	if b := byID["b"]; b.Class != workflowdef.ClaimFailed || b.Answers["reachable"] != "reachable" || len(b.ScanGroupIDs) != 1 {
		t.Fatalf("b = %+v, want the challenge's answers and the kept group link", b)
	}
	if c := byID["c"]; c.Class != workflowdef.ClaimOpen || !c.Dropped {
		t.Fatalf("c = %+v, want open because the challenge left it out", c)
	}
	if d := byID["d"]; d.Title != "New advisory" || d.Class != workflowdef.ClaimOpen || d.Origin != "challenge" {
		t.Fatalf("d = %+v", d)
	}
}

func documentFacts(t *testing.T) ReportDocumentFacts {
	return ReportDocumentFacts{
		Brief: securityBrief(),
		Claims: []RunClaim{
			{ID: "held", Class: workflowdef.ClaimHeld},
			{ID: "adjudicated", Class: workflowdef.ClaimHeld, Answers: map[string]string{"reachable": "reachable", "outcome": "degraded", "attacker": "anyone_remote"}},
			{ID: "open", Class: workflowdef.ClaimOpen, ScanGroupIDs: []string{"group:a"}},
		},
	}
}

func validDocument() guidance.CoordinatorCompletionReport {
	return guidance.CoordinatorCompletionReport{
		Synthesis: "done",
		Findings: []guidance.CoordinatorFinding{
			{ID: "adjudicated", Title: "Rated by the review", Disposition: "act"},
			{ID: "open", Title: "Still open", Disposition: "unresolved"},
			{Title: "Sound surface", Disposition: "held"},
			{ID: "new-risk", Title: "Additional risk", Disposition: "accept", Answers: map[string]string{"reachable": workflowdef.BriefUnknown, "outcome": "degraded", "attacker": "anyone_remote"}},
		},
		Ask: &guidance.CoordinatorAsk{Do: "Approve the fix.", Effort: "small"},
	}
}

// firstIssue is the requirement a repair addresses first.
func firstIssue(report guidance.CoordinatorCompletionReport, facts ReportDocumentFacts) guidance.ReportDocumentIssue {
	if issues := CheckReportDocument(report, facts); len(issues) > 0 {
		return issues[0]
	}
	return guidance.ReportDocumentIssue{}
}

// Every failing requirement is listed, so a stored report names all of them.
func TestCheckReportDocument_ListsEveryFailingRequirement(t *testing.T) {
	facts := documentFacts(t)
	facts.Claims = append(facts.Claims, RunClaim{ID: "refuted", Class: workflowdef.ClaimFailed})
	doc := validDocument()
	doc.Ask = nil
	var codes []string
	for _, issue := range CheckReportDocument(doc, facts) {
		codes = append(codes, issue.Code)
	}
	want := []string{guidance.ReportDocumentInvalidCode, guidance.ReportClaimUnreportedCode}
	if !slices.Equal(codes, want) {
		t.Fatalf("codes = %v, want %v", codes, want)
	}
}

// A document that states a disposition, answers, and an ask for every finding
// that needs them stands.
func TestCheckReportDocument_ValidDocumentStands(t *testing.T) {
	if issue := firstIssue(validDocument(), documentFacts(t)); issue.Code != "" {
		t.Fatalf("issue = %+v, want none", issue)
	}
}

// Each field the first page needs is required in the shape it reads.
func TestCheckReportDocument_RefusesMissingFields(t *testing.T) {
	cases := map[string]func(*guidance.CoordinatorCompletionReport){
		"no disposition":      func(r *guidance.CoordinatorCompletionReport) { r.Findings[2].Disposition = "" },
		"unknown disposition": func(r *guidance.CoordinatorCompletionReport) { r.Findings[2].Disposition = "ok" },
		"held with answers": func(r *guidance.CoordinatorCompletionReport) {
			r.Findings[2].Answers = map[string]string{"reachable": "reachable"}
		},
		"unrated attention":     func(r *guidance.CoordinatorCompletionReport) { r.Findings[3].Answers = nil },
		"answers over a review": func(r *guidance.CoordinatorCompletionReport) { r.Findings[0].Answers = r.Findings[3].Answers },
		"act without an ask":    func(r *guidance.CoordinatorCompletionReport) { r.Ask = nil },
		"ask without do":        func(r *guidance.CoordinatorCompletionReport) { r.Ask.Do = "" },
		"ask effort off-list":   func(r *guidance.CoordinatorCompletionReport) { r.Ask.Effort = "tiny" },
		"set-aside without reason": func(r *guidance.CoordinatorCompletionReport) {
			r.SetAsides = []guidance.CoordinatorSetAside{{Scanner: "lycaon-secrets"}}
		},
		"set-aside naming nothing": func(r *guidance.CoordinatorCompletionReport) {
			r.SetAsides = []guidance.CoordinatorSetAside{{Reason: "fixtures"}}
		},
	}
	for name, edit := range cases {
		doc := validDocument()
		edit(&doc)
		if issue := firstIssue(doc, documentFacts(t)); issue.Code != guidance.ReportDocumentInvalidCode {
			t.Fatalf("%s: issue = %+v, want %s", name, issue, guidance.ReportDocumentInvalidCode)
		}
	}
	noRating := documentFacts(t)
	noRating.Brief = nil
	doc := validDocument()
	if issue := firstIssue(doc, noRating); issue.Code != guidance.ReportDocumentInvalidCode {
		t.Fatalf("answers without a declared rating: issue = %+v", issue)
	}
}

// An authored severity must name the level the finding's answers decide, by
// its label or its declared tone; a review claim's answers decide for a
// finding sharing its id, and leaving severity out always stands.
func TestCheckReportDocument_SeverityNamesTheRatedLevel(t *testing.T) {
	facts := documentFacts(t)
	refused := func(doc guidance.CoordinatorCompletionReport) bool {
		issue := firstIssue(doc, facts)
		return issue.Code == guidance.ReportDocumentInvalidCode && strings.Contains(issue.Reason, "contradicts the level")
	}
	doc := validDocument()
	doc.Findings[0].Severity = "critical"
	if !refused(doc) {
		t.Fatal("a severity above the review's Low level stood")
	}
	doc.Findings[0].Severity = "low"
	if issue := firstIssue(doc, facts); issue.Code != "" {
		t.Fatalf("issue = %+v, want the review's level accepted", issue)
	}

	doc = validDocument()
	doc.Findings[3].Answers = map[string]string{"reachable": "reachable", "outcome": "code_runs", "attacker": "already_inside"}
	for _, sev := range []string{"Moderate", "medium", ""} {
		doc.Findings[3].Severity = sev
		if issue := firstIssue(doc, facts); issue.Code != "" {
			t.Fatalf("severity %q: issue = %+v, want the Moderate level accepted by label or tone", sev, issue)
		}
	}
	doc.Findings[3].Severity = "high"
	if !refused(doc) {
		t.Fatal("a severity the Moderate level does not declare stood")
	}
}

// Every claim the review left open or overturned is carried by a finding with
// the claim's id.
func TestCheckReportDocument_CarriesUnsettledClaims(t *testing.T) {
	facts := documentFacts(t)
	facts.Claims = append(facts.Claims, RunClaim{ID: "refuted", Class: workflowdef.ClaimFailed})
	issue := firstIssue(validDocument(), facts)
	if issue.Code != guidance.ReportClaimUnreportedCode || issue.Count != 1 || !strings.Contains(issue.Offenders[0], "refuted (failed)") {
		t.Fatalf("issue = %+v, want the overturned claim named", issue)
	}
}

// A finding that carries a claim's id without a title is refused for the
// title it lacks; the claim it carries is not reported as uncarried.
func TestCheckReportDocument_UntitledFindingNamesItsTitle(t *testing.T) {
	facts := documentFacts(t)
	facts.Claims = append(facts.Claims, RunClaim{ID: "refuted", Class: workflowdef.ClaimFailed})
	doc := validDocument()
	doc.Findings = append(doc.Findings, guidance.CoordinatorFinding{ID: "refuted", Disposition: "held"})
	issues := CheckReportDocument(doc, facts)
	if len(issues) != 1 || issues[0].Code != guidance.ReportDocumentInvalidCode || !strings.Contains(issues[0].Reason, `"refuted" has no title`) {
		t.Fatalf("issues = %+v, want only the missing title named", issues)
	}
}

func inventoryGroup(id, scanner string, paths ...string) scanfindings.InventoryGroup {
	return scanfindings.InventoryGroup{ID: id, Scanner: scanner, RuleID: "rule", Level: api.FindingLevelHigh, Paths: paths}
}

// Every scanner group is assessed or set aside; ids that name no group in the
// run are refused once the scans have settled.
func TestCheckReportDocument_AccountsForTheInventory(t *testing.T) {
	facts := documentFacts(t)
	facts.Inventory = RunInventory{Settled: true, Groups: []scanfindings.InventoryGroup{
		inventoryGroup("group:a", "sca", "go.mod"),
		inventoryGroup("group:b", "secrets", "internal/x_test.go"),
		inventoryGroup("group:c", "secrets", "internal/y_test.go", "cmd/main.go"),
	}}
	issue := firstIssue(validDocument(), facts)
	if issue.Code != guidance.ReportInventoryUnaccountedCode || issue.Count != 2 {
		t.Fatalf("issue = %+v, want two unaccounted groups", issue)
	}

	doc := validDocument()
	doc.SetAsides = []guidance.CoordinatorSetAside{{Scanner: "secrets", Paths: []string{"**/*_test.go"}, Reason: "test fixtures"}}
	issue = firstIssue(doc, facts)
	if issue.Code != guidance.ReportInventoryUnaccountedCode || issue.Count != 1 || !strings.Contains(issue.Offenders[0], "group:c") {
		t.Fatalf("issue = %+v, want only the group reported outside the fixtures", issue)
	}
	if !strings.Contains(issue.Offenders[0], "cmd/main.go") || !strings.Contains(issue.Offenders[0], "internal/y_test.go") {
		t.Fatalf("offender = %q, want every reported place of the split group listed", issue.Offenders[0])
	}

	doc.Findings[0].ScanGroupIDs = []string{"group:c"}
	if issue := firstIssue(doc, facts); issue.Code != "" {
		t.Fatalf("issue = %+v, want every group accounted for", issue)
	}

	doc.Findings[0].ScanGroupIDs = []string{"e865229e-f77c-48c3-b3a9-8a7dacb15338"}
	issue = firstIssue(doc, facts)
	if issue.Code != guidance.ReportInventoryUnaccountedCode || issue.Offenders[0] != "e865229e-f77c-48c3-b3a9-8a7dacb15338" {
		t.Fatalf("issue = %+v, want the id outside the inventory named", issue)
	}

	doc.Findings[0].ScanGroupIDs = []string{"group:c"}
	doc.SetAsides = append(doc.SetAsides, guidance.CoordinatorSetAside{Scanner: "sast", Reason: "unused"})
	if issue := firstIssue(doc, facts); issue.Code != guidance.ReportDocumentInvalidCode {
		t.Fatalf("issue = %+v, want a set-aside that accounts for nothing refused", issue)
	}
}

type fakeInventory struct {
	run   []api.CodeScan
	other map[string]api.CodeScan
}

func (f fakeInventory) RunScans(context.Context, string) ([]api.CodeScan, error) { return f.run, nil }

func (f fakeInventory) Scan(_ context.Context, id string) (*api.CodeScan, error) {
	for _, s := range f.run {
		if s.ID == id {
			return &s, nil
		}
	}
	if s, ok := f.other[id]; ok {
		return &s, nil
	}
	return nil, nil
}

func secretFinding(path string) api.SecurityFinding {
	return api.SecurityFinding{
		RuleID: "gitleaks:generic-api-key", Level: api.FindingLevelHigh,
		Locations: []api.SecurityFindingLocation{{URI: path}},
		Tool:      api.ToolDescriptor{DriverID: "lycaon-secrets"},
	}
}

// A cited scan id is always wrong, and the refusal names the groups that scan
// holds in the run; an unknown id is wrong once the run's scans are terminal.
func TestCheckScanGroups(t *testing.T) {
	finding := secretFinding("a_test.go")
	group := scanfindings.FindingGroupID(finding)
	inv := fakeInventory{
		run: []api.CodeScan{{ID: "scan-run", ScannerID: "lycaon-secrets", Status: api.CodeScanStatusComplete, Findings: []api.SecurityFinding{finding}}},
		other: map[string]api.CodeScan{
			"scan-ambient": {ID: "scan-ambient", ScannerID: "lycaon-secrets", Status: api.CodeScanStatusComplete, Findings: []api.SecurityFinding{finding}},
		},
	}
	ctx := context.Background()
	got, err := CheckScanGroups(ctx, inv, "run", []string{group})
	testutil.FailErr(t, "check held group", err)
	if !got.OK() {
		t.Fatalf("check = %+v, want the run's group accepted", got)
	}
	got, err = CheckScanGroups(ctx, inv, "run", []string{"scan-ambient", "group:none"})
	testutil.FailErr(t, "check bad ids", err)
	if got.OK() || len(got.ScanIDs["scan-ambient"]) != 1 || got.ScanIDs["scan-ambient"][0] != group || len(got.Unknown) != 1 {
		t.Fatalf("check = %+v, want the scan id mapped to its group and the unknown id named", got)
	}
	inv.run[0].Status = api.CodeScanStatusRunning
	got, err = CheckScanGroups(ctx, inv, "run", []string{"group:none"})
	testutil.FailErr(t, "check while running", err)
	if !got.OK() {
		t.Fatalf("check = %+v, want an unknown id accepted while the run's scans still run", got)
	}
}

// securityBrief mirrors the definition package's security brief fixture.
func securityBrief() *workflowdef.Brief {
	return &workflowdef.Brief{
		Question: "How serious is it?",
		Dimensions: []workflowdef.BriefDimension{
			{ID: "reachable", Label: "Reachable", Question: "Does untrusted input reach the flawed code?", AllowUnknown: true,
				Values: []workflowdef.BriefValue{{ID: "reachable", Label: "Yes"}, {ID: "not_reachable", Label: "No"}}},
			{ID: "outcome", Label: "Worst outcome", Question: "What happens if someone uses it?",
				Values: []workflowdef.BriefValue{{ID: "none", Label: "None"}, {ID: "degraded", Label: "Service degraded"}, {ID: "code_runs", Label: "Code runs"}}},
			{ID: "attacker", Label: "Attacker needs", Question: "Who would have to do it?",
				Values: []workflowdef.BriefValue{
					{ID: "anyone_remote", Label: "Anyone remote", Phrase: "anyone who can reach it over the network could use it"},
					{ID: "already_inside", Label: "Already inside", Phrase: "only someone already in control of the app could use it"},
				}},
		},
		Basis: []string{"attacker"},
		Levels: []workflowdef.BriefLevel{
			{Label: "Critical", Means: "Act now.", Tone: "critical", When: []map[string][]string{{"reachable": {"reachable"}, "outcome": {"code_runs"}, "attacker": {"anyone_remote"}}}},
			{Label: "Moderate", Means: "Fix it on a normal schedule.", Tone: "medium", When: []map[string][]string{{"reachable": {"reachable"}, "outcome": {"code_runs"}, "attacker": {"already_inside"}}}},
			{Label: "Low", Means: "Nothing urgent.", Tone: "low", When: []map[string][]string{{"reachable": {"reachable"}, "outcome": {"degraded"}}}},
			{Label: "None", Means: "Nothing found that needs action.", Tone: "good"},
		},
	}
}

func TestReportChecksEveryUnratedFindingAndInventoryTogether(t *testing.T) {
	facts := ReportDocumentFacts{Brief: securityBrief(), Inventory: RunInventory{
		Settled: true, Groups: []scanfindings.InventoryGroup{{ID: "group:missing", Paths: []string{"src/only.go"}}},
	}}
	doc := guidance.CoordinatorCompletionReport{
		Findings: []guidance.CoordinatorFinding{
			{ID: "c1", Title: "First", Disposition: "act"},
			{ID: "c2", Title: "Second", Disposition: "accept"},
		},
	}
	issues := CheckReportDocument(doc, facts)
	if len(issues) != 4 {
		t.Fatalf("issues = %+v, want both findings, ask, and inventory", issues)
	}
	for i, id := range []string{"c1", "c2"} {
		if !strings.Contains(issues[i].Reason, id) || !strings.Contains(issues[i].Reason, "answers") {
			t.Fatalf("finding %s missing from issues: %+v", id, issues)
		}
	}
	if issue := issues[3]; issue.Code != guidance.ReportInventoryUnaccountedCode || !strings.Contains(issue.Offenders[0], "src/only.go") {
		t.Fatalf("inventory repair lost the single location: %+v", issue)
	}
}
