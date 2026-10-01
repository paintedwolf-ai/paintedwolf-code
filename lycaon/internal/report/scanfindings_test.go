package report

import (
	"strings"
	"testing"
)

// The scan note accounts for every row the scanners reported: what the report
// lists, what the scan withheld, and what fell below the listing floor.
func TestScanScopeNote_AccountsForEveryReportedRow(t *testing.T) {
	closed := &ReportScan{Scanners: []string{"opengrep"}, Total: 21, Stored: 17, Listed: 17, Ignored: 2, Merged: 4}
	note := scanScopeNote(closed, 17, 3)
	for _, want := range []string{"17 rows under 3 rules", "opengrep reported 21 findings", "2 ignored by this project", "4 merged"} {
		if !strings.Contains(note, want) {
			t.Fatalf("scan note %q: want substring %q", note, want)
		}
	}
	for _, banned := range []string{"withheld at ingest", "below the listing floor"} {
		if strings.Contains(note, banned) {
			t.Fatalf("scan note %q: the arithmetic closes, nothing is %q", note, banned)
		}
	}
}

// A budgeted scan and a floored listing are different withholdings, and the
// note names each rather than leaving a total the rows cannot explain.
func TestScanScopeNote_NamesBothWithholdings(t *testing.T) {
	scan := &ReportScan{Total: 20491, Stored: 16929, Listed: 400, Merged: 3562}
	note := scanScopeNote(scan, 400, 40)
	for _, want := range []string{"400 rows under 40 rules", "3562 merged", "16529 not represented by listed groups"} {
		if !strings.Contains(note, want) {
			t.Fatalf("scan note %q: want substring %q", note, want)
		}
	}

	budgeted := &ReportScan{Total: 60, Stored: 47, Listed: 47}
	if note := scanScopeNote(budgeted, 47, 5); !strings.Contains(note, "13 withheld at ingest") {
		t.Fatalf("scan note %q: want the rows the scan never stored named", note)
	}
}

// A scan that stored everything it found has nothing to disclaim.
func TestScanScopeNote_SilentWhenNothingWasWithheld(t *testing.T) {
	scan := &ReportScan{Total: 3, Stored: 3, Listed: 3, ByLevel: map[string]int{"high": 3}}
	if note := scanScopeNote(scan, 3, 1); note != "3 rows under 1 rule · 3 findings reported · 3 high" {
		t.Fatalf("scan note = %q, want only the tally when every row is listed", note)
	}
}

// A rule that fires in hundreds of files states the first of them and counts
// the rest: the ledger holds the full set.
func TestLocationRuns_CapsFilesAndCountsTheRest(t *testing.T) {
	var rows []ReportScanRow
	for i := 0; i < maxFilesPerRule+5; i++ {
		rows = append(rows, ReportScanRow{
			Severity: "info", RuleID: "r",
			File: string(rune('a'+i)) + ".go", Line: i + 1,
		})
	}
	got := runsText(locationRuns(rows))
	if strings.Contains(got, "o.go") {
		t.Fatalf("locations %q: want the files past the cap withheld", got)
	}
	if !strings.Contains(got, "and 5 locations in 5 further files") {
		t.Fatalf("locations %q: want the withheld files counted", got)
	}
}

// Grouping keeps the most severe grade a rule fired at, orders by rank, and
// drops the scanner prefix only where the scanner is known.
func TestGroupScanRows_OneGroupPerRule(t *testing.T) {
	rows := []ReportScanRow{
		{Severity: "info", RuleID: "opengrep:generic.bad-words", File: "a.go", Line: 1, Message: "todo"},
		{Severity: "medium", RuleID: "opengrep:shell.missing-pipefail", File: "b.sh", Line: 2, Message: "pipefail"},
		{Severity: "high", RuleID: "opengrep:go.weak-comparison", File: "c.go", Line: 3, Message: "weak"},
		{Severity: "info", RuleID: "opengrep:generic.bad-words", File: "d.go", Line: 4, Message: "todo"},
	}
	rules := []ReportScanRule{
		{ID: "opengrep:go.weak-comparison", Scanner: "opengrep"},
		{ID: "opengrep:shell.missing-pipefail", Scanner: "opengrep"},
	}

	groups := groupScanRows(rows, rules)
	var ids []string
	for _, g := range groups {
		ids = append(ids, g.rule.DisplayID())
	}
	if strings.Join(ids, ",") != "go.weak-comparison,shell.missing-pipefail,opengrep:generic.bad-words" {
		t.Fatalf("group order = %v, want severity rank first, prefix dropped only where the scanner is known", ids)
	}
	if n := len(groups[2].rows); n != 2 {
		t.Fatalf("bad-words rows = %d, want both folded into one group", n)
	}
	if desc := groups[2].rule.Description; desc != "todo" {
		t.Fatalf("shared description = %q, want the one message every row carries", desc)
	}
}

func runsText(runs []inlineRun) string {
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(r.Text)
	}
	return b.String()
}
