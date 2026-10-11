package review

import (
	"encoding/json"
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoverageVerdictCitationsJoinGrounding(t *testing.T) {
	def := workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"coverage": workflowdef.VerdictCoverageType}}
	review := api.CoverageReview{Revision: "current", Assessments: []api.CoverageAssessment{{ID: "auth", CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "file#1"}}}}}
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	got := allVerdictCitations(def, map[string]string{"coverage": string(raw)}, []api.CitationGroundingCitedEvidence{{Handle: "scan#1"}})
	if len(got) != 2 || got[1].Handle != "file#1" {
		t.Fatalf("coverage citations bypassed audit: %+v", got)
	}
	for _, raw := range []string{`null`, `[]`, `{} {}`, `{"unexpected":true}`} {
		if _, err := workflowvalidation.ParseVerdictCoverage(def, map[string]string{"coverage": raw}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestLegEvidenceQualifiesHandlesBySession(t *testing.T) {
	tasks := map[string]api.WorkerTask{
		"t1": {ID: "t1", ChildSessionID: "child-a", Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{Findings: []api.WorkerCompletionFinding{{Path: "a.go", Evidence: "read#1"}, {Path: "a.go", Evidence: "read#1"}, {Path: "b.go"}}}}},
		"t2": {ID: "t2", Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{Findings: []api.WorkerCompletionFinding{{Path: "c.go", Evidence: "grep#2"}}}}},
	}
	got := legEvidence([]string{"t1", "t2", "missing"}, tasks)
	if len(got) != 1 || got[0] != "child-a:read#1" {
		t.Fatalf("leg evidence = %v", got)
	}
}
