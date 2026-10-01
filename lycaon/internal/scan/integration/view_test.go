package integration

import (
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestParseScanView(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "summary", false},
		{"summary", "summary", false},
		{"FULL", "full", false},
		{"invalid_view", "", true},
	} {
		got, err := scan.ParseScanView(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ParseScanView(%q) want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseScanView(%q) err = %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseScanView(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestApplyScanViewFullKeepsFindings(t *testing.T) {
	in := &wire.CodeScan{
		ID:            "s1",
		FindingsCount: 2,
		Findings: []wire.SecurityFinding{{
			RuleID:  "r1",
			Level:   wire.FindingLevelHigh,
			Message: "test",
			Locations: []wire.SecurityFindingLocation{{
				URI: "src/a.go",
			}},
			Fingerprints: wire.SecurityFindingFingerprints{Primary: "abc123"},
			Tool:         wire.ToolDescriptor{DriverID: "test-scanner", Name: "test-scanner"},
		}},
		Result: []byte(`{"findings_count":2}`),
	}
	full := scan.ApplyScanView(in, "full")
	if full == nil || len(full.Findings) != 1 || full.Result != nil {
		t.Fatalf("full view = %#v", full)
	}
	summary := scan.ApplyScanView(in, "summary")
	if summary == nil || len(summary.Findings) != 0 {
		t.Fatalf("summary view leaked findings: %#v", summary)
	}
}

func TestScanWarningsAreAggregatedOnlyForPresentation(t *testing.T) {
	warnings := []wire.ScanWarning{
		{Kind: wire.ScanWarningFilePartialSemantics, File: "a.py", StartLine: 3, Construct: "unsupported"},
		{Kind: wire.ScanWarningFilePartialSemantics, File: "a.py", StartLine: 5, Construct: "unsupported"},
		{Kind: wire.ScanWarningFilePartialSemantics, File: "b.py", StartLine: 3, Construct: "unsupported"},
		{Kind: wire.ScanWarningRuleParseError, RuleID: "broken-rule"},
	}
	warnings = append(warnings, warnings[0])
	in := &wire.CodeScan{ID: "limited", CoverageStatus: wire.ScanCoveragePartial, Warnings: warnings}
	summary := scan.ApplyScanView(in, "summary")
	if len(summary.Warnings) != 0 || len(summary.WarningSummary) != 2 || summary.CoverageStatus != wire.ScanCoveragePartial {
		t.Fatalf("summary loses coverage or leaks diagnostic rows: %+v", summary)
	}
	first := summary.WarningSummary[0]
	if first.Kind != wire.ScanWarningFilePartialSemantics || first.Count != 3 || first.Files != 2 || first.Rules != 0 {
		t.Fatalf("semantic diagnostic counts: %+v", first)
	}
	if repeated := scan.ApplyScanView(summary, "summary"); len(repeated.WarningSummary) != 2 {
		t.Fatalf("summary projection is not stable: %+v", repeated)
	}
	full := scan.ApplyScanView(in, "full")
	if len(full.Warnings) != 5 || len(in.Warnings) != 5 || full.CoverageStatus != wire.ScanCoveragePartial {
		t.Fatalf("presentation changed audit rows or coverage: %+v", full)
	}
}
