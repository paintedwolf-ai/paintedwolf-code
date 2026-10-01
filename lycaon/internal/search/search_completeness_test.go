package search

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectToolMessageIndexesRejectedOutcome(t *testing.T) {
	msg := api.Message{
		ID:      "tool-rej",
		Role:    api.MessageRoleTool,
		Content: "Rejected: READ_PATH_NOT_FOUND\nCode: READ_PATH_NOT_FOUND",
		ToolResult: &api.ToolResult{
			Outcome:    api.ToolResultOutcomeRejected,
			ToolCallID: "call-1",
			Content:    "Rejected: READ_PATH_NOT_FOUND\nCode: READ_PATH_NOT_FOUND",
		},
		CreatedAt: time.Now().UTC(),
	}
	rows := ProjectToolMessage("proj", "sess", msg, "read")
	if len(rows) == 0 {
		t.Fatal("expected rejected tool result indexed")
	}
	if !strings.Contains(rows[0].Snippet, "READ_PATH_NOT_FOUND") {
		t.Fatalf("snippet = %q", rows[0].Snippet)
	}
}

func TestProjectLifecycleEvidenceCheckpointDecisionSubject(t *testing.T) {
	msg := api.Message{
		ID:   "chk-1",
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			ToolCallID: "call-1",
			CheckpointDecision: &api.CheckpointDecisionMeta{
				CheckpointID: "cp-1",
				Kind:         api.CheckpointKindToolApproval,
				Status:       api.CheckpointStatusApproved,
				Tool:         "command",
				Subject:      "git push origin main",
			},
		},
		CreatedAt: time.Now().UTC(),
	}
	rows := ProjectLifecycleEvidence("proj", "sess", msg)
	if len(rows) != 1 || rows[0].Kind != "checkpoint_decision" {
		t.Fatalf("rows = %+v", rows)
	}
	if !strings.Contains(rows[0].Snippet, "git push origin main") {
		t.Fatalf("snippet = %q", rows[0].Snippet)
	}
	if !strings.Contains(rows[0].Snippet, "command") {
		t.Fatalf("snippet missing tool: %q", rows[0].Snippet)
	}
}

func TestProjectLifecycleEvidenceWorkflowBoundary(t *testing.T) {
	msg := api.Message{
		ID:   "b1",
		Role: api.MessageRoleSystem,
		Kind: api.MessageKindWorkflowBoundary,
		WorkflowBoundary: &api.WorkflowBoundaryMeta{
			Event: "paused",
			Phase: "review",
		},
		CreatedAt: time.Now().UTC(),
	}
	rows := ProjectLifecycleEvidence("proj", "sess", msg)
	if len(rows) != 1 || rows[0].Kind != "workflow_boundary" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestProjectLifecycleEvidenceWorkerManifestWithoutGrounding(t *testing.T) {
	msg := api.Message{
		ID:      "w1",
		Role:    api.MessageRoleTool,
		Content: `{"proof":{"changed_paths":["src/a.go"]}}`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "job-1",
			Status:   api.WorkerSummaryStatusCanceled,
		},
		CreatedAt: time.Now().UTC(),
	}
	rows := ProjectLifecycleEvidence("proj", "sess", msg)
	if len(rows) == 0 {
		t.Fatal("expected worker manifest rows")
	}
}

func TestProjectLifecycleEvidenceWorkerManifestXMLEnvelope(t *testing.T) {
	content := `<task job_id="job-1" state="canceled">
  <summary>Worker leg canceled</summary>
  <task_result>canceled</task_result>
  <proof_json>{"changed_paths":["src/a.go","src/b.go"]}</proof_json>
  <report_json>{"files_modified":["src/c.go"],"findings":[{"path":"src/d.go","evidence":"handle-1"}]}</report_json>
</task>`
	msg := api.Message{
		ID:      "w-xml",
		Role:    api.MessageRoleTool,
		Content: content,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "job-1",
			Status:   api.WorkerSummaryStatusCanceled,
		},
		CreatedAt: time.Now().UTC(),
	}
	rows := ProjectLifecycleEvidence("proj", "sess", msg)
	if len(rows) < 3 {
		t.Fatalf("expected structured path/finding rows from XML envelope, got %+v", rows)
	}
	var sawA, sawFinding bool
	for _, row := range rows {
		if row.Path == "src/a.go" {
			sawA = true
		}
		if row.Path == "src/d.go" && strings.Contains(row.Snippet, "handle-1") {
			sawFinding = true
		}
	}
	if !sawA || !sawFinding {
		t.Fatalf("missing structured evidence rows: %+v", rows)
	}
}

func TestProjectDraftVersionIndexesOutcome(t *testing.T) {
	rows := ProjectDraftVersion("proj", "sess", "slot-1", api.DraftVersion{
		VersionIndex: 0,
		Body:         "rejected draft body",
		OutcomeCode:  "INVEST_HANDLE_NOT_OBSERVED",
		CreatedAt:    time.Now().UTC(),
	})
	if len(rows) != 1 || rows[0].HintCode != "INVEST_HANDLE_NOT_OBSERVED" {
		t.Fatalf("rows = %+v", rows)
	}
}
