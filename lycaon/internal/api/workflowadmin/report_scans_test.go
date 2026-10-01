package workflowadmin

import (
	"fmt"
	"strings"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// The scan section accounts for every reported row and carries each rule once.
func TestSummarizeScans_RulesAndCoverage(t *testing.T) {
	scans := []wire.CodeScan{{
		ScannerID: "opengrep", HeadSHA: "a1b2c3d",
		FindingsCount: 60, FindingsMerged: 13,
		Ignored:         []wire.ScanIgnoredFinding{{RuleID: "r", Reason: "accepted"}},
		FindingsByLevel: map[string]int{"medium": 10, "info": 37},
		Findings: []wire.SecurityFinding{
			{RuleID: "shell.missing-pipefail", Level: wire.FindingLevelMedium, Message: "no pipefail",
				Tool:      wire.ToolDescriptor{DriverID: "opengrep"},
				Locations: []wire.SecurityFindingLocation{{URI: "scripts/a.sh", StartLine: 12}}},
			{RuleID: "shell.missing-pipefail", Level: wire.FindingLevelMedium, Message: "no pipefail",
				Tool:      wire.ToolDescriptor{DriverID: "opengrep"},
				Locations: []wire.SecurityFindingLocation{{URI: "scripts/b.sh", StartLine: 3}}},
			{RuleID: "generic.bad-words", Level: wire.FindingLevelInfo, Message: "TODO here",
				Tool:      wire.ToolDescriptor{DriverID: "opengrep"},
				Locations: []wire.SecurityFindingLocation{{URI: "README.md", StartLine: 4}}},
		},
	}}

	rows, rules, scan, head := summarizeScans(scans)
	// The info row falls below the listing floor: it is counted, not listed.
	if len(rows) != 2 || head != "a1b2c3d" {
		t.Fatalf("rows = %+v head = %q, want only the rows at or above the floor", rows, head)
	}
	if len(rules) != 1 || rules[0].ID != "shell.missing-pipefail" {
		t.Fatalf("rules = %+v, want one entry per listed rule id", rules)
	}
	if rules[0].Scanner != "opengrep" || rules[0].Description != "no pipefail" {
		t.Fatalf("rule[0] = %+v, want the shared message carried once", rules[0])
	}
	if scan.Total != 60 || scan.Stored != 3 || scan.Listed != 2 || scan.Ignored != 1 || scan.Merged != 13 {
		t.Fatalf("scan = %+v, want every term of the coverage arithmetic", scan)
	}
	// An ignored row is stored, so it is not part of what the scan withheld.
	if got := scan.WithheldAtIngest(); got != 44 {
		t.Fatalf("withheld at ingest = %d, want the rows the scan never stored", got)
	}
	if got := scan.NotListed(); got != 1 {
		t.Fatalf("not listed = %d, want the stored row below the listing floor", got)
	}
}

// The budget takes the most severe rows across every scan, so a late scanner
// keeps its rules rather than being named as run with nothing listed.
func TestSummarizeScans_BudgetTakesTheMostSevereAcrossScans(t *testing.T) {
	medium := func(rule string) wire.SecurityFinding {
		return wire.SecurityFinding{
			RuleID: rule, Level: wire.FindingLevelMedium, Message: "m",
			Tool:      wire.ToolDescriptor{DriverID: "first"},
			Locations: []wire.SecurityFindingLocation{{URI: rule + ".go", StartLine: 1}},
		}
	}
	var bulk []wire.SecurityFinding
	for i := 0; i < maxListedScanGroups; i++ {
		bulk = append(bulk, medium(fmt.Sprintf("bulk-%d", i)))
	}
	scans := []wire.CodeScan{
		{ScannerID: "first", FindingsCount: len(bulk), Findings: bulk},
		{ScannerID: "second", FindingsCount: 1, Findings: []wire.SecurityFinding{{
			RuleID: "late.critical", Level: wire.FindingLevelCritical, Message: "c",
			Tool:      wire.ToolDescriptor{DriverID: "second"},
			Locations: []wire.SecurityFindingLocation{{URI: "late.go", StartLine: 9}},
		}}},
	}

	rows, rules, scan, _ := summarizeScans(scans)
	if len(rows) != maxListedScanGroups {
		t.Fatalf("rows = %d, want the budget exactly", len(rows))
	}
	if rows[0].RuleID != "late.critical" {
		t.Fatalf("first row = %+v, want the most severe row first", rows[0])
	}
	var sawLate bool
	for _, r := range rules {
		if r.ID == "late.critical" {
			sawLate = true
		}
	}
	if !sawLate {
		t.Fatal("the later scan's rule was dropped by the budget while its scanner is still named")
	}
	if scan.NotListed() != 1 {
		t.Fatalf("not listed = %d, want the one row the budget displaced", scan.NotListed())
	}
}

// A scanner's message can be a remediation essay with embedded newlines, which
// render as missing glyphs. The rule states its first sentence, on one line.
func TestSummarizeScans_RuleDescriptionIsOneSentenceOnOneLine(t *testing.T) {
	_, rules, _, _ := summarizeScans([]wire.CodeScan{{
		ScannerID: "opengrep", FindingsCount: 1,
		Findings: []wire.SecurityFinding{{
			RuleID: "ssrf", Level: wire.FindingLevelMedium,
			Message: "Server-Side Request Forgery exploits backend systems.\nEnsure user input is not used directly.\n" +
				"```\nfunc SafeTransport() {}\n```",
			Locations: []wire.SecurityFindingLocation{{URI: "a.go", StartLine: 1}},
		}},
	}})
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want one", rules)
	}
	got := rules[0].Description
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("description = %q, want no newlines", got)
	}
	if got != "Server-Side Request Forgery exploits backend systems." {
		t.Fatalf("description = %q, want only the first sentence", got)
	}
}

// Rules whose findings disagree about the message carry none, so each row
// keeps its own rather than one standing for all.
func TestSummarizeScans_NoSharedDescriptionWhenMessagesDiffer(t *testing.T) {
	_, rules, _, _ := summarizeScans([]wire.CodeScan{{
		ScannerID: "osv", FindingsCount: 2,
		Findings: []wire.SecurityFinding{
			{RuleID: "sca.advisory", Level: wire.FindingLevelHigh, Message: "CVE-2026-1 in libfoo",
				Locations: []wire.SecurityFindingLocation{{URI: "go.mod", StartLine: 3}}},
			{RuleID: "sca.advisory", Level: wire.FindingLevelHigh, Message: "CVE-2026-2 in libbar",
				Locations: []wire.SecurityFindingLocation{{URI: "go.mod", StartLine: 9}}},
		},
	}})
	if len(rules) != 1 || rules[0].Description != "" {
		t.Fatalf("rules = %+v, want no shared description when the rows differ", rules)
	}
	if rules[0].Scanner != "osv" {
		t.Fatalf("rule scanner = %q, want the scan's scanner when the finding names none", rules[0].Scanner)
	}
}
