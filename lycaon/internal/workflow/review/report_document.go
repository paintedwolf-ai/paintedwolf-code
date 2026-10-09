package review

import (
	"fmt"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReportDocumentFacts are what the document is checked against.
type ReportDocumentFacts struct {
	Brief     *workflowdef.Brief
	Claims    []workflowpresentation.RunClaim
	Inventory RunInventory
	// SetAsides are those the run's review phases recorded.
	SetAsides []scanfindings.SetAside
}

// ReportLinkedGroups are the scanner groups the run's review assessed: every
// group a claim or a finding cites.
func ReportLinkedGroups(report guidance.CoordinatorCompletionReport, claims []workflowpresentation.RunClaim) []string {
	var out []string
	for _, c := range claims {
		out = append(out, c.ScanGroupIDs...)
	}
	for _, f := range report.Findings {
		out = append(out, f.ScanGroupIDs...)
	}
	return out
}

// ReportSetAsides converts the closeout's set-asides for the scan plane.
func ReportSetAsides(report guidance.CoordinatorCompletionReport) []scanfindings.SetAside {
	out := make([]scanfindings.SetAside, 0, len(report.SetAsides))
	for _, sa := range report.SetAsides {
		out = append(out, scanfindings.SetAside{GroupIDs: sa.ScanGroupIDs, Scanner: sa.Scanner, Paths: sa.Paths, Reason: sa.Reason})
	}
	return out
}

// CheckReportDocument returns document defects in repair order.
func CheckReportDocument(report guidance.CoordinatorCompletionReport, facts ReportDocumentFacts) []guidance.ReportDocumentIssue {
	out := checkReportFindings(report, facts)
	for _, issue := range []guidance.ReportDocumentIssue{
		checkReportAsk(report),
		checkReportSetAsides(report),
		checkClaimsCarried(report, facts.Claims),
		checkInventoryAccounted(report, facts),
	} {
		if issue.Code != "" {
			out = append(out, issue)
		}
	}
	return out
}

func invalid(format string, args ...any) guidance.ReportDocumentIssue {
	return guidance.ReportDocumentIssue{Code: guidance.ReportDocumentInvalidCode, Reason: fmt.Sprintf(format, args...)}
}

// ClaimAnswers maps each claim the review answered to its answers; a finding
// sharing the claim's id is rated by them.
func ClaimAnswers(claims []workflowpresentation.RunClaim) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, c := range claims {
		if len(c.Answers) > 0 {
			out[c.ID] = c.Answers
		}
	}
	return out
}

func checkReportFindings(report guidance.CoordinatorCompletionReport, facts ReportDocumentFacts) []guidance.ReportDocumentIssue {
	adjudicated := ClaimAnswers(facts.Claims)
	var out []guidance.ReportDocumentIssue
	for _, f := range report.Findings {
		for _, claim := range facts.Claims {
			if f.ID != claim.ID {
				continue
			}
			if claim.Class == workflowdef.ClaimOpen && f.Disposition != "unresolved" {
				out = append(out, invalid("finding %q is an open question; use disposition unresolved", f.ID))
			}
			if claim.Class != workflowdef.ClaimOpen && f.Disposition == "unresolved" {
				out = append(out, invalid("finding %q contradicts its resolved claim", f.ID))
			}
		}
		if issue := checkReportFinding(f, facts.Brief, adjudicated); issue.Code != "" {
			out = append(out, issue)
		}
	}
	return out
}

func checkReportFinding(f guidance.CoordinatorFinding, brief *workflowdef.Brief, adjudicated map[string]map[string]string) guidance.ReportDocumentIssue {
	name := findingName(f)
	if strings.TrimSpace(f.Title) == "" {
		return invalid("finding %s has no title; every finding states its conclusion in one line as `title`", name)
	}
	switch api.CompletionReportFindingDisposition(f.Disposition) {
	case api.CompletionReportFindingDispositionAct, api.CompletionReportFindingDispositionAccept:
	case api.CompletionReportFindingDispositionHeld, api.CompletionReportFindingDispositionUnresolved:
		if len(f.Answers) > 0 {
			return invalid("finding %s is %s, so it is not rated; remove its answers", name, f.Disposition)
		}
		return guidance.ReportDocumentIssue{}
	case "":
		return invalid("finding %s has no disposition (want %s)", name, alternatives(api.AllCompletionReportFindingDispositions()))
	default:
		return invalid("finding %s has disposition %q (want %s)", name, f.Disposition, alternatives(api.AllCompletionReportFindingDispositions()))
	}
	if brief == nil {
		if len(f.Answers) > 0 {
			return invalid("finding %s has answers, but this workflow declares no rating questions", name)
		}
		return guidance.ReportDocumentIssue{}
	}
	answers, isAdjudicated := adjudicated[strings.TrimSpace(f.ID)]
	if isAdjudicated {
		if len(f.Answers) > 0 {
			return invalid("finding %s shares its id with a claim whose answers the review adjudicated; remove the finding's answers", name)
		}
	} else {
		answers = f.Answers
		if err := brief.CheckAnswers(answers); err != nil {
			return invalid("finding %s answers: %v; answer %s", name, err, brief.DescribeAnswers())
		}
	}
	if issue := checkRatedSeverity(name, f.Severity, brief, answers); issue.Code != "" {
		return issue
	}
	return guidance.ReportDocumentIssue{}
}

// checkRatedSeverity refuses an authored severity that names a level other
// than the one the finding's answers decide. The report states the decided
// level either way; a contradicting word means the prose disagrees with it.
func checkRatedSeverity(name, severity string, brief *workflowdef.Brief, answers map[string]string) guidance.ReportDocumentIssue {
	sev := strings.TrimSpace(severity)
	if sev == "" {
		return guidance.ReportDocumentIssue{}
	}
	worst, _ := brief.LevelRange(brief.Rateable(answers))
	if worst < 0 || worst >= len(brief.Levels) {
		return guidance.ReportDocumentIssue{}
	}
	level := brief.Levels[worst]
	if strings.EqualFold(sev, level.Label) || (level.Tone != "" && strings.EqualFold(sev, level.Tone)) {
		return guidance.ReportDocumentIssue{}
	}
	return invalid("finding %s severity %q contradicts the level its answers decide (%s); omit severity, the host states the level", name, severity, level.Label)
}

func findingName(f guidance.CoordinatorFinding) string {
	if id := strings.TrimSpace(f.ID); id != "" {
		return fmt.Sprintf("%q", id)
	}
	return fmt.Sprintf("%q", f.Title)
}

func checkReportAsk(report guidance.CoordinatorCompletionReport) guidance.ReportDocumentIssue {
	acting := 0
	for _, f := range report.Findings {
		if api.CompletionReportFindingDisposition(f.Disposition) == api.CompletionReportFindingDispositionAct {
			acting++
		}
	}
	ask := report.Ask
	if ask == nil {
		if acting > 0 {
			return invalid("%d finding(s) need action, so the report needs an ask: {do, effort, why}", acting)
		}
		return guidance.ReportDocumentIssue{}
	}
	if ask.Do == "" {
		return invalid("ask.do is empty; state the decision the reader is asked to make")
	}
	if efforts := api.AllCompletionReportAskEfforts(); !slices.Contains(efforts, api.CompletionReportAskEffort(ask.Effort)) {
		return invalid("ask.effort is %q (want %s)", ask.Effort, alternatives(efforts))
	}
	return guidance.ReportDocumentIssue{}
}

func checkReportSetAsides(report guidance.CoordinatorCompletionReport) guidance.ReportDocumentIssue {
	for i, sa := range report.SetAsides {
		if sa.Reason == "" {
			return invalid("set_asides[%d] needs a reason", i)
		}
		if len(sa.ScanGroupIDs) == 0 && sa.Scanner == "" && len(sa.Paths) == 0 {
			return invalid("set_asides[%d] names no groups: give scan_group_ids, or a scanner and paths selector", i)
		}
	}
	return guidance.ReportDocumentIssue{}
}

// UnreportedClaims are the claims a review left open or overturned that no
// finding carries by id: conclusions a report owes its reader.
func UnreportedClaims(findingIDs []string, claims []workflowpresentation.RunClaim) []workflowpresentation.RunClaim {
	carried := map[string]bool{}
	for _, id := range findingIDs {
		carried[strings.TrimSpace(id)] = true
	}
	var out []workflowpresentation.RunClaim
	for _, c := range claims {
		if c.Class == workflowdef.ClaimHeld || carried[c.ID] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// checkClaimsCarried requires a finding for every claim the review left open
// or overturned, sharing the claim's id, so the document states what it means.
func checkClaimsCarried(report guidance.CoordinatorCompletionReport, claims []workflowpresentation.RunClaim) guidance.ReportDocumentIssue {
	ids := make([]string, 0, len(report.Findings))
	for _, f := range report.Findings {
		ids = append(ids, f.ID)
	}
	var missing []string
	for _, c := range UnreportedClaims(ids, claims) {
		missing = append(missing, fmt.Sprintf("%s (%s)", c.ID, c.Class))
	}
	if len(missing) == 0 {
		return guidance.ReportDocumentIssue{}
	}
	sort.Strings(missing)
	return guidance.ReportDocumentIssue{
		Code:      guidance.ReportClaimUnreportedCode,
		Reason:    "claims the review left open or overturned need a finding with the same id",
		Offenders: sampleStrings(missing, 8),
		Count:     len(missing),
	}
}

func checkInventoryAccounted(report guidance.CoordinatorCompletionReport, facts ReportDocumentFacts) guidance.ReportDocumentIssue {
	issue, _ := inventoryAccounting(report, facts)
	return issue
}

// inventoryAccounting returns the accounting defect and the groups left unaccounted.
func inventoryAccounting(report guidance.CoordinatorCompletionReport, facts ReportDocumentFacts) (guidance.ReportDocumentIssue, []scanfindings.InventoryGroup) {
	inv := facts.Inventory
	if len(inv.Groups) == 0 {
		return guidance.ReportDocumentIssue{}, nil
	}
	reviewed := len(facts.SetAsides)
	sets := append(append([]scanfindings.SetAside(nil), facts.SetAsides...), ReportSetAsides(report)...)
	account := scanfindings.AccountInventory(inv.Groups, ReportLinkedGroups(report, facts.Claims), sets)
	if inv.Settled && len(account.Unknown) > 0 {
		return guidance.ReportDocumentIssue{
			Code:      guidance.ReportInventoryUnaccountedCode,
			Reason:    "these scan_group_ids name no group in this run's scans",
			Offenders: sampleStrings(account.Unknown, 8),
			Count:     len(account.Unknown),
		}, nil
	}
	for i, n := range account.SetAsideCounts[reviewed:] {
		if n == 0 {
			return invalid("set_asides[%d] (%q) accounts for no group in this run's scans", i, report.SetAsides[i].Reason), nil
		}
	}
	left := account.Unaccounted()
	if len(left) == 0 {
		return guidance.ReportDocumentIssue{}, nil
	}
	offenders := make([]string, 0, min(len(left), 8))
	for _, g := range left[:min(len(left), 8)] {
		offenders = append(offenders, formatInventoryOffender(g))
	}
	return guidance.ReportDocumentIssue{
		Code:      guidance.ReportInventoryUnaccountedCode,
		Reason:    fmt.Sprintf("%d of %d scanner groups have no assessment and no set-aside", len(left), len(inv.Groups)),
		Offenders: offenders,
		Count:     len(left),
	}, left
}

// Every location matters when repairing a path selector for a whole group.
func formatInventoryOffender(g scanfindings.InventoryGroup) string {
	line := fmt.Sprintf("%s · %s · %s · %s", g.ID, g.Scanner, g.Level, g.RuleID)
	if len(g.Paths) == 0 {
		return line
	}
	return line + " · " + strings.Join(g.Paths, "; ")
}

func sampleStrings(in []string, n int) []string {
	if len(in) <= n {
		return append([]string(nil), in...)
	}
	return append([]string(nil), in[:n]...)
}

// alternatives lists enum values as prose: "a, b, or c".
func alternatives[T ~string](values []T) string {
	words := make([]string, len(values))
	for i, v := range values {
		words[i] = string(v)
	}
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + ", or " + words[len(words)-1]
}
