package workercompletion

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerCoverageSurvivesDurableRoundTripAndRequiresObservedEvidence(t *testing.T) {
	review := &api.CoverageReview{Revision: "scope", Assessments: []api.CoverageAssessment{{ID: "gap", Disposition: "immaterial", Reason: "Excluded scope", CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "read#missing"}}}}}
	report := WorkerCompletionReport{LegStatus: "complete", Brief: "Reviewed scope", CoverageReview: review}
	raw, err := json.Marshal(ReportWire(report))
	testutil.FailErr(t, "encode durable report", err)
	var stored api.WorkerCompletionReport
	testutil.FailErr(t, "decode durable report", json.Unmarshal(raw, &stored))
	restored := ReportFromWire(&stored)
	if !reflect.DeepEqual(restored.CoverageReview, review) {
		t.Fatal("coverage lost through persistence")
	}
	parent := restored.ParentReport()
	if !reflect.DeepEqual(parent.CoverageReview, review) {
		t.Fatal("presentation truncated coverage contract")
	}
	citations := workerReportCitations(restored)
	evaluation := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, citations, nil, guidance.WorkerNarrativeInput{}, evidence.Ledger{})
	if evaluation.Code == "" {
		t.Fatal("coverage bypassed citation audit")
	}
}
