package output

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanfixture "github.com/lycaon/lycaon/internal/scan/testfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func exportEntry(state api.FindingLedgerState, rule string, ignore *api.FindingIgnore, advisories ...string) api.FindingLedgerEntry {
	finding := scanfindings.FixtureFinding(rule, api.FindingLevelHigh, rule+" message", "internal/api/a.go", 7)
	finding.Tool.DriverID = "opengrep-sast"
	if len(advisories) > 0 {
		finding = scanfixture.WithAdvisory(finding, advisories...)
	}
	return api.FindingLedgerEntry{
		Finding: finding, State: state, ScannerID: "opengrep-sast", Ignore: ignore,
	}
}

// Ignores export as SARIF suppressions on the results they cover.
func TestExportLedgerSARIFCarriesDecisionsAsSuppressions(t *testing.T) {
	t.Parallel()
	data, err := ExportLedgerSARIF([]api.FindingLedgerEntry{
		exportEntry(api.FindingLedgerIgnored, "rule-ignored", &api.FindingIgnore{
			EntryID: "e1", Reason: "fixture material", MatchedOn: "path: test/**",
		}),
		exportEntry(api.FindingLedgerOpen, "rule-open", nil),
	})
	testutil.FailErr(t, "export sarif", err)

	var doc struct {
		Runs []struct {
			Results []struct {
				RuleID       string `json:"ruleId"`
				Suppressions []struct {
					Kind          string `json:"kind"`
					Justification string `json:"justification"`
				} `json:"suppressions"`
			} `json:"results"`
		} `json:"runs"`
	}
	testutil.FailErr(t, "decode sarif", json.Unmarshal(data, &doc))
	if len(doc.Runs) != 1 || len(doc.Runs[0].Results) != 2 {
		t.Fatalf("runs = %+v, want one run with both results", doc.Runs)
	}
	byRule := map[string]int{}
	for _, result := range doc.Runs[0].Results {
		byRule[result.RuleID] = len(result.Suppressions)
		if result.RuleID == "rule-ignored" {
			if result.Suppressions[0].Kind != "external" {
				t.Fatalf("suppression kind = %q, want external", result.Suppressions[0].Kind)
			}
			if result.Suppressions[0].Justification != "fixture material" {
				t.Fatalf("justification = %q, want the recorded reason", result.Suppressions[0].Justification)
			}
		}
	}
	if byRule["rule-ignored"] != 1 || byRule["rule-open"] != 0 {
		t.Fatalf("suppressions landed on the wrong results: %+v", byRule)
	}
}

// Lapsed ignores are not exported as suppressions.
func TestExportLedgerSARIFOmitsALapsedDecision(t *testing.T) {
	t.Parallel()
	data, err := ExportLedgerSARIF([]api.FindingLedgerEntry{
		exportEntry(api.FindingLedgerOpen, "rule-a", &api.FindingIgnore{
			EntryID: "e1", Reason: "was temporary", ExpiresOn: "2026-01-01", Expired: true,
		}),
	})
	testutil.FailErr(t, "export sarif", err)
	if got := string(data); strings.Contains(got, "suppressions") {
		t.Fatalf("a lapsed decision was published as a suppression:\n%s", got)
	}
}

// OpenVEX covers advisory findings only and omits unestablished absences.
func TestExportLedgerOpenVEXStatesOnlyWhatItCan(t *testing.T) {
	t.Parallel()
	entries := []api.FindingLedgerEntry{
		exportEntry(api.FindingLedgerOpen, "rule-lint", nil),
		exportEntry(api.FindingLedgerOpen, "rule-open", nil, "GHSA-open", "CVE-2026-00001"),
		exportEntry(api.FindingLedgerFixed, "rule-fixed", nil, "GHSA-fixed", "CVE-2026-00002"),
		exportEntry(api.FindingLedgerNotObserved, "rule-gone", nil, "GHSA-gone", "CVE-2026-00003"),
		exportEntry(api.FindingLedgerIgnored, "rule-vex", &api.FindingIgnore{
			EntryID: "e1", Reason: "the affected codec is never reached",
			Justification: api.FindingIgnoreJustificationVulnerableCodeNotInExecutePath,
		}, "GHSA-vex", "CVE-2026-00004"),
	}
	data, err := ExportLedgerOpenVEX(entries, "acme/widget", time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))
	testutil.FailErr(t, "export openvex", err)

	var doc struct {
		Context    string `json:"@context"`
		Statements []struct {
			Vulnerability struct {
				Name    string   `json:"name"`
				Aliases []string `json:"aliases"`
			} `json:"vulnerability"`
			Status        string `json:"status"`
			Justification string `json:"justification"`
		} `json:"statements"`
	}
	testutil.FailErr(t, "decode openvex", json.Unmarshal(data, &doc))
	if doc.Context != vexContext {
		t.Fatalf("@context = %q, want the OpenVEX namespace", doc.Context)
	}

	byName := map[string]string{}
	for _, statement := range doc.Statements {
		byName[statement.Vulnerability.Name] = statement.Status
		// The CVE names the statement.
		if !strings.HasPrefix(statement.Vulnerability.Name, "CVE-") {
			t.Fatalf("statement named %q, want the CVE", statement.Vulnerability.Name)
		}
	}
	want := map[string]string{
		"CVE-2026-00001": "affected",
		"CVE-2026-00002": "fixed",
		"CVE-2026-00004": "not_affected",
	}
	for id, status := range want {
		if byName[id] != status {
			t.Fatalf("%s = %q, want %q", id, byName[id], status)
		}
	}
	// Excludes the lint finding and the not-observed one.
	if len(doc.Statements) != len(want) {
		t.Fatalf("statements = %+v, want only the three that can be stated", doc.Statements)
	}
	for _, statement := range doc.Statements {
		if statement.Status != "not_affected" {
			continue
		}
		if statement.Justification != "vulnerable_code_not_in_execute_path" {
			t.Fatalf("justification = %q, want the recorded OpenVEX value", statement.Justification)
		}
	}
}
