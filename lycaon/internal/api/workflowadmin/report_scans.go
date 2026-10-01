package workflowadmin

import (
	"strings"

	"github.com/lycaon/lycaon/internal/report"
	"github.com/lycaon/lycaon/internal/runeclamp"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A single style rule can fire tens of thousands of times, so the report lists
// rows at or above a severity floor within a row budget and counts the rest.
// Both numbers are stated; the full set stays in the scan ledger.
const (
	scanListingFloor    = "medium"
	maxListedScanGroups = 100
	// maxRuleDescription bounds a rule's own text. Scanners ship remediation
	// essays as the message; the report states the rule, not the essay.
	maxRuleDescription = 220
)

// summarizeScans folds a scan set into the scan section: the rows it lists,
// the rules they reference, and the arithmetic accounting for every row the
// scanners reported.
func summarizeScans(scans []wire.CodeScan) ([]report.ReportScanRow, []report.ReportScanRule, *report.ReportScan, string) {
	if len(scans) == 0 {
		return nil, nil, nil, ""
	}
	summary := &report.ReportScan{ByLevel: map[string]int{}}
	var headSHA string
	var findings []wire.SecurityFinding
	for _, cs := range scans {
		if headSHA == "" {
			headSHA = strings.TrimSpace(cs.HeadSHA)
		}
		summary.Total += cs.FindingsCount
		summary.Stored += len(cs.Findings)
		summary.Ignored += len(cs.Ignored)
		summary.Merged += cs.FindingsMerged
		summary.Scanners = appendScanner(summary.Scanners, cs)
		summary.Executions = append(summary.Executions, report.ReportScanExecution{ID: cs.ID, Scanner: cs.ScannerID, Status: string(cs.Status), Coverage: string(cs.CoverageStatus), Error: cs.Error})
		for level, count := range cs.FindingsByLevel {
			summary.ByLevel[level] += count
		}
		for _, f := range cs.Findings {
			if f.Tool.DriverID == "" {
				f.Tool.DriverID = findingScanner(f, cs)
			}
			findings = append(findings, f)
		}
	}
	groups := scanfindings.GroupFindings(findings, 10)
	summary.Groups = len(groups)
	var rows []report.ReportScanRow
	var rules []report.ReportScanRule
	for _, g := range groups {
		if string(g.Level) != "unknown" && report.SeverityRank(string(g.Level)) > report.SeverityRank(scanListingFloor) {
			continue
		}
		if len(rules) >= maxListedScanGroups {
			break
		}
		rule := report.ReportScanRule{ID: g.RuleID, GroupID: g.ID, Scanner: g.Scanner, Description: ruleDescription(g.Message), Occurrences: g.Count, LocationCount: g.LocationCount}
		rule.Details = reportAdvisoryDetails(g.Advisory)
		if g.Advisory != nil && g.Advisory.Package != nil {
			p := g.Advisory.Package
			rule.Package = p.Ecosystem + ":" + p.Name + "@" + p.Version
		}
		rules = append(rules, rule)
		summary.Represented += g.Count
		if len(g.Locations) == 0 {
			rows = append(rows, report.ReportScanRow{Severity: string(g.Level), RuleID: g.RuleID, GroupID: g.ID})
		}
		for _, loc := range g.Locations {
			rows = append(rows, report.ReportScanRow{Severity: string(g.Level), RuleID: g.RuleID, GroupID: g.ID, File: loc.URI, Line: loc.StartLine, Message: ruleDescription(loc.Message)})
		}
	}
	summary.Listed = len(rows)
	summary.ListedGroups = len(rules)
	summary.Total = max(summary.Total, summary.Stored)
	return rows, rules, summary, headSHA
}

// ruleDescription folds a scanner's message onto one line and keeps its first
// sentence. Newlines in a message render as missing glyphs, and a rule's
// remediation guide is not the report's to reproduce.
func ruleDescription(msg string) string {
	first, _ := splitFirstSentence(msg)
	if len([]rune(first)) <= maxRuleDescription {
		return first
	}
	runes := []rune(first)
	cut := maxRuleDescription
	for i := maxRuleDescription; i > maxRuleDescription*3/4; i-- {
		if runes[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(runes[:cut]), " ,;:") + runeclamp.Marker
}

// findingScanner names the scanner a finding came from: its own tool stamp,
// else the scan's scanner id.
func findingScanner(f wire.SecurityFinding, cs wire.CodeScan) string {
	if id := strings.TrimSpace(f.Tool.DriverID); id != "" {
		return id
	}
	if name := strings.TrimSpace(f.Tool.Name); name != "" {
		return name
	}
	return strings.TrimSpace(cs.ScannerID)
}

// appendScanner names what produced a scan's findings: its scanner id when one
// is recorded, and otherwise the categories it ran under.
func appendScanner(into []string, cs wire.CodeScan) []string {
	if id := strings.TrimSpace(cs.ScannerID); id != "" {
		return appendUnique(into, id)
	}
	for _, cat := range cs.Categories {
		if c := strings.TrimSpace(string(cat)); c != "" {
			into = appendUnique(into, c)
		}
	}
	return into
}

func appendUnique(into []string, value string) []string {
	for _, existing := range into {
		if existing == value {
			return into
		}
	}
	return append(into, value)
}
