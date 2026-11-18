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
	rows := ProjectToolMessage("proj", "sess", msg, "Read")
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one rejected tool row", rows)
	}
	row := rows[0]
	if row.Tool != "read" || row.SourceRef != "call-1" || row.MessageID != "tool-rej" || row.HitKind != HitKindTool {
		t.Fatalf("row = %+v, want tool read, call call-1, message tool-rej", row)
	}
	if row.Snippet != msg.ToolResult.Content {
		t.Fatalf("snippet = %q, want the result content", row.Snippet)
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
	for _, kind := range api.AllWorkflowBoundaryKinds() {
		event := string(kind)
		t.Run(event, func(t *testing.T) {
			msg := api.Message{
				ID:               "b1",
				Role:             api.MessageRoleSystem,
				Kind:             api.MessageKindWorkflowBoundary,
				WorkflowBoundary: &api.WorkflowBoundaryMeta{Event: event, Phase: "review"},
				CreatedAt:        time.Now().UTC(),
			}
			rows := ProjectLifecycleEvidence("proj", "sess", msg)
			if len(rows) != 1 {
				t.Fatalf("rows = %+v, want one boundary row", rows)
			}
			row := rows[0]
			if row.Kind != "workflow_boundary" || row.ID != RowID(SourceMessage, "b1", "boundary:"+event) {
				t.Fatalf("row = %+v", row)
			}
			if want := event + " · review"; row.Snippet != want {
				t.Fatalf("snippet = %q, want %q", row.Snippet, want)
			}
		})
	}
}

func TestProjectLifecycleEvidenceWorkflowFeedback(t *testing.T) {
	cases := []struct {
		name, answer, content, want string
	}{
		{"answer", "  use the second option ", "ignored content", "use the second option"},
		{"content fallback", " ", "approve the plan", "approve the plan"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := api.Message{
				ID:               "f1",
				Role:             api.MessageRoleSystem,
				Kind:             api.MessageKindWorkflowFeedback,
				WorkflowFeedback: &api.WorkflowFeedbackMeta{Answer: tc.answer},
				Content:          tc.content,
				CreatedAt:        time.Now().UTC(),
			}
			rows := ProjectLifecycleEvidence("proj", "sess", msg)
			if len(rows) != 1 || rows[0].Kind != "workflow_feedback" {
				t.Fatalf("rows = %+v, want one feedback row", rows)
			}
			if rows[0].Snippet != tc.want {
				t.Fatalf("snippet = %q, want %q", rows[0].Snippet, tc.want)
			}
		})
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
