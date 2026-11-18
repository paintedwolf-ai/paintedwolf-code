package limits

import (
	"strings"
	"testing"
)

const compactionLimitsFixture = `max_citation_grounding_retries: 7
max_grounding_rejects_per_turn: 3
max_grounding_rejects_per_cycle: 9
max_report_document_retries: 2
max_wait_visibility_repairs: 4
`

func TestParseCompactionLimitsReadsEachLimit(t *testing.T) {
	doc, err := parseCompactionLimits([]byte(compactionLimitsFixture + "max_worker_summary_chars: 1200\n"))
	if err != nil {
		t.Fatalf("parse limits: %v", err)
	}
	want := bundledCompactionLimitsYAML{
		MaxWorkerSummaryChars: 1200, MaxCitationGroundingRetries: 7, MaxGroundingRejectsPerTurn: 3,
		MaxGroundingRejectsPerCycle: 9, MaxReportDocumentRetries: 2, MaxWaitVisibilityRepairs: 4,
	}
	if doc != want {
		t.Fatalf("limits = %+v, want %+v", doc, want)
	}
}

func TestParseCompactionLimitsDefaultsTheWorkerSummaryCap(t *testing.T) {
	doc, err := parseCompactionLimits([]byte(compactionLimitsFixture))
	if err != nil {
		t.Fatalf("parse limits: %v", err)
	}
	if doc.MaxWorkerSummaryChars != baseWorkerSummaryMaxChars {
		t.Fatalf("worker summary cap = %d, want %d", doc.MaxWorkerSummaryChars, baseWorkerSummaryMaxChars)
	}
}

func TestParseCompactionLimitsRefusesAMissingLimit(t *testing.T) {
	for _, key := range []string{
		"max_citation_grounding_retries", "max_grounding_rejects_per_turn", "max_grounding_rejects_per_cycle",
		"max_report_document_retries", "max_wait_visibility_repairs",
	} {
		var kept []string
		for _, line := range strings.Split(compactionLimitsFixture, "\n") {
			if !strings.HasPrefix(line, key+":") {
				kept = append(kept, line)
			}
		}
		if _, err := parseCompactionLimits([]byte(strings.Join(kept, "\n"))); err == nil {
			t.Errorf("limits without %s were accepted", key)
		}
	}
	if _, err := parseCompactionLimits([]byte("max_citation_grounding_retries: [")); err == nil {
		t.Error("malformed limits were accepted")
	}
}
