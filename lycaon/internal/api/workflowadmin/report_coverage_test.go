package workflowadmin

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/report"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// The scan account counts only the run's own inventory: a group is used when a
// claim or finding links it or a set-aside accounts for it, and an empty
// complete scan adds no gap.
func TestScanAccountCountsTheRunsInventory(t *testing.T) {
	finding := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{DriverID: "one", RuleID: "rule", Level: wire.FindingLevelHigh})
	fixture := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{DriverID: "one", RuleID: "fixture", Level: wire.FindingLevelHigh,
		Locations: []wire.SecurityFindingLocation{{URI: "internal/x_test.go"}}})
	scans := []wire.CodeScan{
		{ID: "findings", ScannerID: "one", Status: wire.CodeScanStatusComplete, CoverageStatus: wire.ScanCoverageComplete, Findings: []wire.SecurityFinding{finding, fixture}},
		{ID: "empty", ScannerID: "two", Status: wire.CodeScanStatusComplete, CoverageStatus: wire.ScanCoverageComplete},
	}
	claims := []workflowpresentation.RunClaim{{ID: "claim", ScanGroupIDs: []string{"group:invented"}}}
	var a runAccount
	a.scanAccount(scans, nil, claims, nil)
	if a.inventory == nil || a.inventory.Total != 2 || a.inventory.Unaccounted != 2 {
		t.Fatalf("inventory = %+v, want two unaccounted groups", a.inventory)
	}
	if len(a.gaps) != 1 || a.gaps[0].Kind != report.GapInventoryUnaccounted || a.gaps[0].Count != 2 {
		t.Fatalf("gaps = %+v, want only the unaccounted inventory", a.gaps)
	}
	if len(a.checks) != 1 || a.checks[0].State != report.CheckUnchecked || a.checks[0].Ran != 2 {
		t.Fatalf("checks = %+v, want the scans unused", a.checks)
	}

	claims[0].ScanGroupIDs = []string{scanfindings.FindingGroupID(finding)}
	completion := &wire.CompletionReportMeta{SetAsides: []wire.CompletionReportSetAside{
		{Scanner: "one", Paths: []string{"**/*_test.go"}, Reason: "test fixtures"},
	}}
	a = runAccount{}
	a.scanAccount(scans, completion, claims, nil)
	if len(a.gaps) != 0 || a.inventory.Linked != 1 || a.inventory.SetAside != 1 || a.checks[0].State != report.CheckDone {
		t.Fatalf("account = %+v %+v %+v, want every group used", a.gaps, a.inventory, a.checks)
	}
	if len(a.inventory.SetAsides) != 1 || a.inventory.SetAsides[0].Groups != 1 {
		t.Fatalf("set-asides = %+v", a.inventory.SetAsides)
	}

	// A review phase that set the fixtures aside accounts for them without the closeout restating it.
	reviewed := []scanfindings.SetAside{{Scanner: "one", Paths: []string{"**/*_test.go"}, Reason: "test fixtures"}}
	a = runAccount{}
	a.scanAccount(scans, nil, claims, reviewed)
	if len(a.gaps) != 0 || a.inventory.SetAside != 1 || len(a.inventory.SetAsides) != 1 || a.inventory.SetAsides[0].Reason != "test fixtures" {
		t.Fatalf("review set-aside account = %+v %+v", a.gaps, a.inventory)
	}
}

// Moved files open a gap until rescanned; engine limits are recorded apart;
// a failed scan is a gap of its own.
func TestScanAccountClassifiesCoverageGaps(t *testing.T) {
	scans := []wire.CodeScan{
		{ID: "secrets", ScannerID: "secrets", Status: wire.CodeScanStatusComplete, CoverageStatus: wire.ScanCoveragePartial,
			Warnings: []wire.ScanWarning{{Kind: wire.ScanWarningSourceMoved, File: "a.go"}}},
		{ID: "sast", ScannerID: "sast", Status: wire.CodeScanStatusComplete, CoverageStatus: wire.ScanCoveragePartial,
			Warnings: []wire.ScanWarning{{Kind: wire.ScanWarningFilePartialSemantics, File: "x.rs"}}},
		{ID: "sca", ScannerID: "sca", Status: wire.CodeScanStatusFailed},
	}
	var a runAccount
	a.scanAccount(scans, nil, nil, nil)
	kinds := map[string]report.ReportGap{}
	for _, g := range a.gaps {
		kinds[g.Kind] = g
	}
	if kinds[report.GapScansMoved].Detail != 1 || kinds[report.GapScansStanding].Detail != 1 || kinds[report.GapScansFailed].Count != 1 {
		t.Fatalf("gaps = %+v", a.gaps)
	}
	if got := (report.ReportInput{Gaps: a.gaps}).Completeness(); got != report.CompletenessIncomplete {
		t.Fatalf("completeness = %q, want incomplete for a failed scan", got)
	}
}

// Open claims are unfinished work, and a review phase named for a reader is a
// check counted by where its claims stand.
func TestClaimAccount(t *testing.T) {
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{
		{ID: "claims", ReviewLoop: &workflowdef.ReviewLoopDef{}},
		{ID: "challenge", ReviewLoop: &workflowdef.ReviewLoopDef{ReconcilesPhase: "claims", BriefLabel: "Second opinion"}},
	}}
	claims := []workflowpresentation.RunClaim{
		{ID: "a", Phase: "challenge", Class: workflowdef.ClaimHeld},
		{ID: "b", Phase: "challenge", Class: workflowdef.ClaimFailed},
		{ID: "c", Phase: "claims", Class: workflowdef.ClaimOpen, Dropped: true, Title: "Stale"},
	}
	var a runAccount
	a.claimAccount(manifest, claims)
	if len(a.gaps) != 1 || a.gaps[0].Count != 1 || a.gaps[0].Names[0] != "Stale" {
		t.Fatalf("gaps = %+v", a.gaps)
	}
	if len(a.checks) != 1 {
		t.Fatalf("checks = %+v, want the labelled review only", a.checks)
	}
	if c := a.checks[0]; c.Subject != "Second opinion" || c.Held != 1 || c.Failed != 1 || c.Open != 1 || c.State != report.CheckPartial {
		t.Fatalf("check = %+v", c)
	}
}

func TestReportBudgetsGroupsInsteadOfOccurrences(t *testing.T) {
	var findings []wire.SecurityFinding
	for range 1000 {
		findings = append(findings, scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{DriverID: "one", RuleID: "noisy", Level: wire.FindingLevelHigh, Locations: []wire.SecurityFindingLocation{{URI: "src/a"}}}))
	}
	findings = append(findings, scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{DriverID: "two", RuleID: "important", Level: wire.FindingLevelUnknown, Locations: []wire.SecurityFindingLocation{{URI: "deps.lock"}}}))
	rows, rules, summary, _ := summarizeScans([]wire.CodeScan{{ID: "scan", Findings: findings, FindingsCount: 1001}, {ID: "failed", ScannerID: "other", Status: wire.CodeScanStatusFailed}})
	if len(rules) != 2 || len(rows) != 2 || summary.Represented != 1001 || len(summary.Executions) != 2 {
		t.Fatalf("rows %d; rules %#v; summary %#v", len(rows), rules, summary)
	}
	if summary.NotListed() != 0 {
		t.Fatalf("sampled occurrences counted as absent: %#v", summary)
	}
}

func TestReportAdvisoryDetailsKeepSourceAndFixBoundaries(t *testing.T) {
	score := 7.5
	details := reportAdvisoryDetails(&wire.AdvisoryRef{
		SeveritySource: "osv.cvss", FixedVersions: []string{"1.2.3", "2.0.1"},
		CVSS: []wire.AdvisoryCVSS{{Type: "CVSS_V3", Vector: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N", Score: &score}},
		URLs: []string{"https://example.org/advisory/1", "https://example.org/advisory/2", "https://example.org/advisory/3", "https://example.org/advisory/4"},
	})
	text := strings.Join(details, "\n")
	for _, want := range []string{"osv.cvss", "7.5", "1.2.3, 2.0.1", "compatibility require assessment", "1 additional advisory references"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(text, "Malicious package") || strings.Contains(text, "Advisory ids") {
		t.Fatalf("plain single-id vulnerability grew extra lines: %s", text)
	}
}

func TestReportAdvisoryDetailsNameMalwareAndEveryAlias(t *testing.T) {
	details := reportAdvisoryDetails(&wire.AdvisoryRef{
		OSVID: "MAL-2025-2544", Aliases: []string{"MAL-2025-2544", "GHSA-6vm3-jj99-7229"},
		Kind: wire.AdvisoryKindMaliciousPackage, SeveritySource: "osv.malicious_package",
	})
	text := strings.Join(details, "\n")
	for _, want := range []string{"Malicious package:", "Advisory ids: GHSA-6vm3-jj99-7229, MAL-2025-2544", "osv.malicious_package"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

// Every worker attempt is listed once: a leg's attempts under their leg, and
// every other attempt, including one whose work id no plan names, on its own.
func TestWorkAccountListsEachAttemptOnce(t *testing.T) {
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	manifest, err := manifests.Get("security-survey", "2.0.0")
	testutil.FailErr(t, "manifest", err)
	var phase string
	for _, p := range manifest.PhaseDefs {
		if workflowdef.PhaseHasGate(p, "worker_cycle_ready") {
			phase = p.ID
		}
	}
	if phase == "" {
		t.Fatal("security survey has no fan-out phase")
	}
	plan := runstate.FanoutPlan{Phase: phase, MaxAttempts: 2, Legs: []runstate.FanoutPlanLeg{{ID: "leg-1", AgentType: "security-reviewer", Subject: "Desktop app"}}}
	tasks := []wire.WorkerTask{
		{ID: "t1", AgentType: "security-reviewer", WorkflowPhase: phase, WorkflowWorkID: "leg-1", Status: wire.WorkerStatusFailed},
		{ID: "t2", AgentType: "security-reviewer", WorkflowPhase: phase, WorkflowWorkID: "leg-1", Status: wire.WorkerStatusComplete},
		{ID: "t3", AgentType: "skeptic", WorkflowPhase: "challenge", Status: wire.WorkerStatusComplete},
		{ID: "t4", AgentType: "security-reviewer", WorkflowPhase: phase, WorkflowWorkID: "unplanned", Status: wire.WorkerStatusComplete},
	}
	h := &Reports{
		Runs:    coverageRuns{vars: map[string]any{"fanout_plans": map[string]any{phase: plan}}},
		Workers: coverageWorkers{tasks: tasks},
	}
	var a runAccount
	testutil.FailErr(t, "workAccount", h.workAccount(context.Background(), &a, &wire.WorkflowRun{ID: "run"}, manifest))
	seen := map[string]int{}
	for _, item := range a.coverage {
		for _, id := range []string{"t1", "t2", "t3", "t4"} {
			if strings.Contains(item.Detail, id) {
				seen[id]++
			}
		}
	}
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		if seen[id] != 1 {
			t.Fatalf("attempt %s listed %d times in %+v, want once", id, seen[id], a.coverage)
		}
	}
}

type coverageRuns struct {
	runstate.RunsRepository
	vars map[string]any
}

func (r coverageRuns) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	return r.vars, nil
}

type coverageWorkers struct {
	worker.WorkerQueue
	tasks []wire.WorkerTask
}

func (w coverageWorkers) ListByWorkflowRunID(context.Context, string, ...wire.WorkerStatus) ([]wire.WorkerTask, error) {
	return w.tasks, nil
}

func TestScanAccountCountsDistinctPathsAndSetAsides(t *testing.T) {
	finding := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{DriverID: "sast", RuleID: "fixture", Level: wire.FindingLevelHigh, Locations: []wire.SecurityFindingLocation{{URI: "fixture.go"}}})
	var scans []wire.CodeScan
	for _, id := range []string{"sast", "sca", "secrets"} {
		scans = append(scans, wire.CodeScan{ID: id, ScannerID: id, Status: wire.CodeScanStatusComplete, CoverageStatus: wire.ScanCoveragePartial, Warnings: []wire.ScanWarning{{Kind: wire.ScanWarningSourceMoved, File: "fixture.go"}}})
	}
	scans[0].Findings = []wire.SecurityFinding{finding}
	id := scanfindings.FindingGroupID(finding)
	var account runAccount
	account.scanAccount(scans, nil, nil, []scanfindings.SetAside{{GroupIDs: []string{id}, Reason: "fixture"}, {GroupIDs: []string{id}, Reason: "reviewed fixture"}})
	if len(account.gaps) != 1 || account.gaps[0].Detail != 1 || account.gaps[0].Count != 3 {
		t.Fatalf("paths counted per scanner: %+v", account.gaps)
	}
	if len(account.inventory.SetAsides) != 1 || account.inventory.SetAsides[0].Groups != 1 {
		t.Fatalf("overlapping exclusions counted twice: %+v", account.inventory)
	}
}

func TestReportCoverageShowsHostCheckForPartialLeg(t *testing.T) {
	phase := "plan"
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{
		{
			ID:    phase,
			Gates: []string{"worker_cycle_ready"},
		},
	}}
	plan := runstate.FanoutPlan{
		Phase:       phase,
		MaxAttempts: 2,
		Legs: []runstate.FanoutPlanLeg{
			{ID: "leg-1", AgentType: "security-reviewer", Subject: "Review auth"},
		},
	}
	tasks := []wire.WorkerTask{
		{
			ID:             "t1",
			AgentType:      "security-reviewer",
			WorkflowPhase:  phase,
			WorkflowWorkID: "leg-1",
			Status:         wire.WorkerStatusComplete,
			Result: &wire.WorkerResult{
				Status:   "partial",
				HintCode: "WORKER_EVIDENCE_HANDLE_UNKNOWN",
				CompletionReport: &wire.WorkerCompletionReport{
					LegStatus: "partial",
				},
			},
		},
		{
			ID:            "helper-1",
			AgentType:     "scout",
			WorkflowPhase: phase,
			Status:        wire.WorkerStatusComplete,
			Result: &wire.WorkerResult{
				Status:   "partial",
				HintCode: "WORKER_EVIDENCE_HANDLE_UNKNOWN",
				CompletionReport: &wire.WorkerCompletionReport{
					LegStatus: "partial",
				},
			},
		},
	}
	h := &Reports{
		Runs:    coverageRuns{vars: map[string]any{"fanout_plans": map[string]any{phase: plan}}},
		Workers: coverageWorkers{tasks: tasks},
	}
	var a runAccount
	testutil.FailErr(t, "workAccount", h.workAccount(context.Background(), &a, &wire.WorkflowRun{ID: "run"}, manifest))

	var legItem, helperItem *report.ReportCoverageItem
	for i := range a.coverage {
		if strings.Contains(a.coverage[i].Subject, "leg-1") {
			legItem = &a.coverage[i]
		}
		if strings.Contains(a.coverage[i].Subject, "scout") {
			helperItem = &a.coverage[i]
		}
	}
	if legItem == nil {
		t.Fatalf("missing coverage item for leg-1 in %+v", a.coverage)
	}
	if legItem.Status != "partial" {
		t.Fatalf("legItem.Status = %q, want partial", legItem.Status)
	}
	if !strings.Contains(legItem.Detail, " · host check: Evidence handles") {
		t.Fatalf("legItem.Detail = %q, want host check suffix", legItem.Detail)
	}

	if helperItem == nil {
		t.Fatalf("missing coverage item for helper in %+v", a.coverage)
	}
	if helperItem.Status != "partial" {
		t.Fatalf("helperItem.Status = %q, want partial", helperItem.Status)
	}
	if !strings.Contains(helperItem.Detail, " · host check: Evidence handles") {
		t.Fatalf("helperItem.Detail = %q, want host check suffix", helperItem.Detail)
	}
}
