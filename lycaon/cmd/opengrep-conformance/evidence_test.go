package main

import (
	"testing"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEvidenceComparisonRejectsWrongAndAbsentFlow(t *testing.T) {
	location := api.SecurityFindingLocation{URI: "source/page.html", StartLine: 2, StartColumn: 9, EndLine: 2, EndColumn: 13}
	span := expectedSpan{Line: 2, Column: 9, EndLine: 2, EndColumn: 13}
	expected := []expectedEvidence{{Rule: "code", Primary: span, Source: &span, Sink: &span}}
	finding := api.SecurityFinding{RuleID: "opengrep:code", Locations: []api.SecurityFindingLocation{location}, Dataflow: &api.SecurityFindingDataflow{Source: &api.SecurityFindingCallTrace{Location: location}, Sink: &api.SecurityFindingCallTrace{Location: location}}}
	report := &scanReport{Parsed: &scanoutput.Result{Findings: []api.SecurityFinding{finding}}}
	if err := compareCaseEvidence(report, location.URI, expected); err != nil {
		t.Fatalf("valid evidence: %v", err)
	}
	report.Parsed.Findings[0].Dataflow.Source.Location.StartColumn++
	if err := compareCaseEvidence(report, location.URI, expected); err == nil {
		t.Fatal("wrong source evidence accepted")
	}
	report.Parsed.Findings[0].Dataflow = nil
	if err := compareCaseEvidence(report, location.URI, expected); err == nil {
		t.Fatal("missing flow accepted")
	}
}

func TestCommentVariantShiftsEvidenceWithoutChangingOriginal(t *testing.T) {
	span := expectedSpan{Line: 2, Column: 9, EndLine: 3, EndColumn: 13}
	original := []expectedEvidence{{Rule: "code", Primary: span, Source: &span, Sink: &span}}
	shifted := shiftCaseEvidence(original)
	if shifted[0].Primary.Line != 3 || shifted[0].Primary.EndLine != 4 || shifted[0].Source.Line != 3 || shifted[0].Sink.EndLine != 4 {
		t.Fatalf("shifted evidence=%+v", shifted)
	}
	if original[0].Primary.Line != 2 || original[0].Source.Line != 2 || original[0].Sink.EndLine != 3 {
		t.Fatal("comment variant changed base evidence")
	}
}
