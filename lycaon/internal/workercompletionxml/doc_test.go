package workercompletionxml_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/workercompletionxml"
)

// A block persisted with the job attribute still reads; the attribute is
// ignored.
func TestUnmarshalProofAndReportJSON(t *testing.T) {
	const raw = `<task worker="fixer" job_id="j1" state="complete">
  <summary>ok</summary>
  <task_result>body</task_result>
  <proof_json>{"changed_paths":["a.go"]}</proof_json>
  <report_json>{"files_modified":["b.go"]}</report_json>
</task>`
	doc, ok := workercompletionxml.Unmarshal(raw)
	if !ok {
		t.Fatal("Unmarshal returned false")
	}
	if doc.Worker != "fixer" || doc.State != "complete" {
		t.Fatalf("attrs = worker=%q state=%q", doc.Worker, doc.State)
	}
	if doc.ProofJSON == "" || doc.ReportJSON == "" {
		t.Fatalf("expected proof/report JSON, got proof=%q report=%q", doc.ProofJSON, doc.ReportJSON)
	}
}

func TestUnmarshalRejectsNonTask(t *testing.T) {
	if _, ok := workercompletionxml.Unmarshal(`{"not":"xml"}`); ok {
		t.Fatal("expected false for non-task content")
	}
}
