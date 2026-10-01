package output_test

import (
	"strings"
	"testing"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParseOutputFindingsJSONGolden(t *testing.T) {
	raw := []byte(`{
		"format": 1,
		"findings": [
			{
				"rule_id": "my-tool:sql-injection",
				"level": "high",
				"message": "possible SQL injection",
				"locations": [{"uri": "app/db.py", "start_line": 42}]
			},
			{
				"rule_id": "no-colon-rule",
				"level": "low",
				"unknown_field": "ignored"
			}
		],
		"trailing_unknown": true
	}`)
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserFindingsJSON, raw)
	testutil.FailErr(t, "scan.ParseOutput failed", err)
	if res.FindingsCount != 2 || len(res.Findings) != 2 {
		t.Fatalf("findings = %d", len(res.Findings))
	}
	first := res.Findings[0]
	if first.RuleID != "my-tool:sql-injection" {
		t.Fatalf("rule id = %q", first.RuleID)
	}
	if first.Level != api.FindingLevelHigh {
		t.Fatalf("level = %q", first.Level)
	}
	if first.Message != "possible SQL injection" {
		t.Fatalf("message = %q", first.Message)
	}
	if first.Locations[0].URI != "app/db.py" || first.Locations[0].StartLine != 42 {
		t.Fatalf("location = %+v", first.Locations[0])
	}
	if first.Fingerprints.Primary == "" {
		t.Fatal("expected fingerprints.primary")
	}
	if res.Findings[1].Level != api.FindingLevelLow {
		t.Fatalf("low level = %q", res.Findings[1].Level)
	}
}

func TestParseOutputFindingsJSONEmptyFindings(t *testing.T) {
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserFindingsJSON, []byte(`{"format":1,"findings":[]}`))
	testutil.FailErr(t, "scan.ParseOutput failed", err)
	if res.FindingsCount != 0 || len(res.Findings) != 0 {
		t.Fatalf("findings = %d", len(res.Findings))
	}
}

func TestParseOutputFindingsJSONBadFormat(t *testing.T) {
	for name, raw := range map[string]string{
		"missing_format": `{"findings":[]}`,
		"wrong_format":   `{"format":2,"findings":[]}`,
	} {
		if _, err := scanoutput.ParseOutput(scanoutput.OutputParserFindingsJSON, []byte(raw)); err == nil {
			t.Fatalf("%s: expected error", name)
		} else if !strings.Contains(err.Error(), "format") {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

func TestParseOutputFindingsJSONMissingFindings(t *testing.T) {
	_, err := scanoutput.ParseOutput(scanoutput.OutputParserFindingsJSON, []byte(`{"format":1}`))
	if err == nil || !strings.Contains(err.Error(), "findings array is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseOutputFindingsJSONRowCoercion(t *testing.T) {
	// Invalid rows are skipped or normalized independently.
	cases := map[string]struct {
		raw       string
		wantCount int
		wantLevel api.FindingLevel
		wantLine  int
	}{
		"missing_rule_id_skipped": {
			raw:       `{"format":1,"findings":[{"level":"high"}]}`,
			wantCount: 0,
		},
		"missing_level_defaults_unknown": {
			raw:       `{"format":1,"findings":[{"rule_id":"r"}]}`,
			wantCount: 1,
			wantLevel: api.FindingLevelUnknown,
		},
		"unknown_level_defaults_unknown": {
			raw:       `{"format":1,"findings":[{"rule_id":"r","level":"catastrophic"}]}`,
			wantCount: 1,
			wantLevel: api.FindingLevelUnknown,
		},
		"negative_start_line_clamped": {
			raw:       `{"format":1,"findings":[{"rule_id":"r","level":"low","locations":[{"uri":"f","start_line":-1}]}]}`,
			wantCount: 1,
			wantLevel: api.FindingLevelLow,
			wantLine:  0,
		},
	}
	for name, tc := range cases {
		res, err := scanoutput.ParseOutput(scanoutput.OutputParserFindingsJSON, []byte(tc.raw))
		testutil.FailErr(t, name+": parse", err)
		if res.FindingsCount != tc.wantCount || len(res.Findings) != tc.wantCount {
			t.Fatalf("%s: findings = %d, want %d", name, res.FindingsCount, tc.wantCount)
		}
		if tc.wantCount == 0 {
			continue
		}
		if res.Findings[0].Level != tc.wantLevel {
			t.Fatalf("%s: level = %q, want %q", name, res.Findings[0].Level, tc.wantLevel)
		}
		if res.Findings[0].Locations[0].StartLine != tc.wantLine {
			t.Fatalf("%s: start_line = %d, want %d", name, res.Findings[0].Locations[0].StartLine, tc.wantLine)
		}
	}
}

func TestParseOutputFindingsJSONBadRowKeepsGoodRows(t *testing.T) {
	// An invalid level does not discard other findings.
	raw := []byte(`{"format":1,"findings":[
		{"rule_id":"good","level":"high","locations":[{"uri":"a.py","start_line":7}]},
		{"rule_id":"weird","level":"blocker"}
	]}`)
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserFindingsJSON, raw)
	testutil.FailErr(t, "parse", err)
	if res.FindingsCount != 2 {
		t.Fatalf("findings = %d, want 2", res.FindingsCount)
	}
	if res.Findings[0].RuleID != "good" || res.Findings[0].Level != api.FindingLevelHigh {
		t.Fatalf("good row = %+v", res.Findings[0])
	}
	if res.Findings[1].RuleID != "weird" || res.Findings[1].Level != api.FindingLevelUnknown {
		t.Fatalf("coerced row = %+v", res.Findings[1])
	}
}

func TestParseOutputFindingsJSONEmptyOutput(t *testing.T) {
	_, err := scanoutput.ParseOutput(scanoutput.OutputParserFindingsJSON, nil)
	if err == nil || !strings.Contains(err.Error(), "empty output") {
		t.Fatalf("error = %v", err)
	}
}
