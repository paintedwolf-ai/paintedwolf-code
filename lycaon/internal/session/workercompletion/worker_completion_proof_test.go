package workercompletion_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildWorkerCompletionProofIncludesReceiptsAndDigest(t *testing.T) {
	receiptOut := surveyreceipt.Attach(`[{"path":"a.go"}]`, surveyreceipt.New("grep", ".", 1, 32, false))
	msgs := []api.Message{
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: "tc1", Name: "grep", Args: map[string]any{"pattern": "foo"}}},
		},
		{Role: api.MessageRoleTool, Content: receiptOut},
	}
	proof := workercompletion.BuildWorkerCompletionProof(msgs, nil, workercompletion.SourceRevision{})
	if proof.ReceiptCount != 1 || len(proof.SurveyTools) != 1 {
		t.Fatalf("proof = %+v", proof)
	}
	digest := workercompletion.FormatWorkerDigest("path-explorer", "job-1", "complete", "survey done", proof, workercompletion.WorkerCompletionReport{})
	if !strings.Contains(digest, "Survey receipts: 1") {
		t.Fatalf("digest = %q", digest)
	}
}

func TestCollectPresentableVisualArtifactIDsSkipsLiveRecording(t *testing.T) {
	msgs := []api.Message{
		{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				Outcome: api.ToolResultOutcomeCompleted,
				Visual:  &api.VisualArtifact{ID: "still-1", Mime: "image/png", Source: api.VisualArtifactSourceCapture},
			},
			Content: `{"artifact_id":"still-1"}`,
		},
		{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				Outcome: api.ToolResultOutcomeCompleted,
				Visual:  &api.VisualArtifact{ID: "live-1", Mime: "video/mp4", Source: api.VisualArtifactSourceCapture, PageID: "page-1"},
			},
			Content: `{"artifact_id":"live-1"}`,
		},
		{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				Outcome: api.ToolResultOutcomeCompleted,
				Visual:  &api.VisualArtifact{ID: "render-1", Mime: "image/png", Source: api.VisualArtifactSourceRender},
			},
			Content: `{"artifact_id":"render-1"}`,
		},
	}
	ids := workercompletion.CollectPresentableVisualArtifactIDs(msgs)
	if len(ids) != 2 || ids[0] != "still-1" || ids[1] != "render-1" {
		t.Fatalf("ids = %#v", ids)
	}
	proof := workercompletion.BuildWorkerCompletionProof(msgs, nil, workercompletion.SourceRevision{})
	if len(proof.VisualArtifactIDs) != 2 {
		t.Fatalf("proof.VisualArtifactIDs = %#v", proof.VisualArtifactIDs)
	}
	digest := workercompletion.FormatWorkerDigest("implementer", "job-v", "complete", "ui done", proof, workercompletion.WorkerCompletionReport{})
	if !strings.Contains(digest, "Visual artifacts") || !strings.Contains(digest, "still-1") {
		t.Fatalf("digest missing visuals: %q", digest)
	}
}

func TestBuildWorkerCompletionProofReadsStatedCommandVerdict(t *testing.T) {
	msgs := []api.Message{{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			Content: "[cmd-1]\n" + `{"ok":true,"exit_code":0,"stages":[{"command":"./ntp_check.py --json","exit_code":0}]}`,
			Invocation: &api.InvocationReceipt{
				ID: "receipt-command", Tool: "command", Status: api.InvocationStatusCompleted,
				Evidence:      api.InvocationEvidence{Kind: "result", Ref: "message-1"},
				SourceVerdict: api.SourceVerdictPassed,
			},
		},
	}}
	proof := workercompletion.BuildWorkerCompletionProof(msgs, nil, workercompletion.SourceRevision{})
	if len(proof.InvocationReceipts) != 1 || proof.InvocationReceipts[0].Verdict != api.SourceVerdictPassed {
		t.Fatalf("command verdict = %+v", proof.InvocationReceipts)
	}
}

func TestBuildWorkerCompletionProofChangedPathsRequireBaseline(t *testing.T) {
	msgs := []api.Message{
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: "tc1", Name: "write", Args: map[string]any{"path": "src/a.go"}}},
		},
		{Role: api.MessageRoleTool, Content: `{"ok":true}`},
	}
	proof := workercompletion.BuildWorkerCompletionProof(msgs, nil, workercompletion.SourceRevision{})
	if len(proof.ChangedPaths) != 0 {
		t.Fatalf("ChangedPaths = %v, want none without leg-start baseline", proof.ChangedPaths)
	}
}
