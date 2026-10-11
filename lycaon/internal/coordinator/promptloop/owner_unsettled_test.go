package promptloop

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"

	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBatchHasUnsettledOwnerFromCodes(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleAssistant, ID: "a1"},
		{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				AssistantMessageID: "a1",
				Outcome:            api.ToolResultOutcomeError,
				Codes:              []string{toolrejection.ToolOwnerFailedCode},
			},
		},
	}
	if !batchHasUnsettledOwner(history, "a1") {
		t.Fatal("expected owner failure on this batch")
	}
	if batchHasUnsettledOwner(history, "other") {
		t.Fatal("other assistant batch must not inherit owner failure")
	}
}

func TestBatchHasUnsettledOwnerFromInvocation(t *testing.T) {
	history := []api.Message{{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			AssistantMessageID: "a1",
			Outcome:            api.ToolResultOutcomeError,
			Invocation: &api.InvocationReceipt{
				Failure: &api.InvocationFailure{Code: toolrejection.ToolOwnerFailedCode, Class: "owner_error"},
			},
		},
	}}
	if !batchHasUnsettledOwner(history, "a1") {
		t.Fatal("expected owner failure from invocation")
	}
}

func TestBatchHasUnsettledOwnerIgnoresCompletedUnverifiable(t *testing.T) {
	history := []api.Message{{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			AssistantMessageID: "a1",
			Outcome:            api.ToolResultOutcomeCompleted,
			Codes:              []string{toolrejection.VerifyUnverifiableCode, isolation.CodeBoundaryRefused},
		},
	}}
	if batchHasUnsettledOwner(history, "a1") {
		t.Fatal("settled unverifiable is not an owner failure")
	}
}

func TestBatchHasUnsettledOwnerIgnoresCompletedOwnerCode(t *testing.T) {
	history := []api.Message{{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			AssistantMessageID: "a1",
			Outcome:            api.ToolResultOutcomeCompleted,
			Codes:              []string{toolrejection.ToolOwnerFailedCode},
		},
	}}
	if batchHasUnsettledOwner(history, "a1") {
		t.Fatal("completed receipt is not an owner failure")
	}
}

func TestBatchHasUnsettledOwnerIgnoresErrorWithoutOwnerCode(t *testing.T) {
	history := []api.Message{{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			AssistantMessageID: "a1",
			Outcome:            api.ToolResultOutcomeError,
		},
	}}
	if batchHasUnsettledOwner(history, "a1") {
		t.Fatal("error without TOOL_OWNER_FAILED is not an owner failure")
	}
}

func TestBatchHasUnsettledOwnerIgnoresHostReject(t *testing.T) {
	history := []api.Message{{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			AssistantMessageID: "a1",
			Outcome:            api.ToolResultOutcomeRejected,
			Codes:              []string{"SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"},
		},
	}}
	if batchHasUnsettledOwner(history, "a1") {
		t.Fatal("host reject is not an owner failure")
	}
}
