package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Refusals carry both the rejected outcome and their code.
func TestRefusalAlwaysWiresOutcomeAndCode(t *testing.T) {
	t.Parallel()
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)

	samples := []struct{ code, body string }{
		{"DISALLOWED_AGENT", "Rejected: DISALLOWED_AGENT\nFix: pick an allowed agent\nCode: DISALLOWED_AGENT"},
		{"COORDINATOR_READ_OUTSIDE_SCOPE", "Rejected: COORDINATOR_READ_OUTSIDE_SCOPE\nCode: COORDINATOR_READ_OUTSIDE_SCOPE"},
		{"SPEC_POSTURE_STUB_REQUIRED", ">>> Spec posture blocked\nCode: SPEC_POSTURE_STUB_REQUIRED"},
	}
	for _, sample := range samples {
		refusal := guidance.NewRefusal(sample.code, sample.body)
		tr := guidance.ComposeToolResult(refusal.Body, refusal.Facts, hints)
		if tr == nil {
			t.Fatalf("ComposeToolResult returned nil for %q", sample.code)
		}
		if tr.Outcome != api.ToolResultOutcomeRejected {
			t.Fatalf("outcome = %q want rejected for %q", tr.Outcome, sample.code)
		}
		if strings.TrimSpace(tr.PrimaryCode()) != sample.code {
			t.Fatalf("codes = %v want %q", tr.Codes, sample.code)
		}
	}
}

func TestApplyTaskDispatchMetadataSetsWireFieldsForEnqueuedTask(t *testing.T) {
	t.Parallel()
	tr := guidance.ComposeToolResult("worker queued", guidance.ToolResultFacts{}, nil)
	dispatch := &api.WorkerDispatch{WorkerID: "550e8400-e29b-41d4-a716-446655440000", AgentType: "implementer"}
	guidance.ApplyTaskDispatchMetadata("task", tr, dispatch)
	if tr.Dispatch == nil || tr.Dispatch.WorkerID != "550e8400-e29b-41d4-a716-446655440000" || tr.Dispatch.AgentType != "implementer" {
		t.Fatalf("dispatch = %#v", tr.Dispatch)
	}
}

func TestApplyTaskDispatchMetadataDoesNotParseContent(t *testing.T) {
	t.Parallel()
	content := `{"agent_type":"implementer","job_id":"550e8400-e29b-41d4-a716-446655440000","status":"enqueued"}
>>> Worker queued
Code: BANNER_TASK_QUEUED`
	tr := guidance.ComposeToolResult(content, guidance.ToolResultFacts{}, nil)
	guidance.ApplyTaskDispatchMetadata("task", tr, nil)
	if tr.Dispatch != nil {
		t.Fatalf("dispatch inferred from content: %#v", tr.Dispatch)
	}
}

func TestApplyTaskDispatchMetadataIgnoresNonDispatchTools(t *testing.T) {
	t.Parallel()
	tr := guidance.ComposeToolResult("read result", guidance.ToolResultFacts{}, nil)
	guidance.ApplyTaskDispatchMetadata("read", tr, &api.WorkerDispatch{WorkerID: "550e8400-e29b-41d4-a716-446655440000"})
	if tr.Dispatch != nil {
		t.Fatalf("dispatch = %#v want nil for read tool", tr.Dispatch)
	}
}
