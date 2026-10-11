package workeroutcomes_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

func batchWorkerSummary(jobID string, status api.WorkerSummaryStatus, report workercompletion.WorkerCompletionReport) *api.WorkerSummaryMeta {
	childSessionID := "child-" + jobID
	agentType := orchestration.ProfileImplementer
	envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:          jobID,
		ChildSessionID: childSessionID,
		AgentType:      agentType,
		State:          string(status),
		Report:         report,
	})
	return &api.WorkerSummaryMeta{
		WorkerID:       jobID,
		ChildSessionID: childSessionID,
		AgentType:      agentType,
		Status:         status,
		Envelope:       envelope,
	}
}

func idleBatchReadyState() surface.ImplementSessionState {
	return surface.ImplementSessionState{
		WorkersInFlight:   0,
		PendingOverlayIDs: []string{},
	}
}

func allTerminalProgress() string {
	return "- [x] implement\n- [x] verify"
}

func inlineEditHistory() []api.Message {
	return []api.Message{
		{Role: api.MessageRoleUser, Content: "fix the handler"},
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: "c1", Name: "edit", Args: map[string]any{
				"path": "internal/handler.go",
			}}},
		},
		{
			Role:    api.MessageRoleTool,
			Content: "ok",
			ToolResult: &api.ToolResult{
				Outcome: api.ToolResultOutcomeCompleted,
				FileEdit: &api.FileEditSnapshot{
					Path: "internal/handler.go",
				},
			},
		},
	}
}

func TestImplementationWorkSinceBoundary_inlineEditOnly(t *testing.T) {
	history := inlineEditHistory()
	since := api.UserIntentBoundary(history)
	if !workeroutcomes.ImplementationWorkSinceBoundary(history, since) {
		t.Fatal("inline edit counts as implementation work")
	}
}

func TestImplementationWorkSinceBoundary_writerWithoutChangedPaths(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "look around"},
		{
			Role: api.MessageRoleAssistant,
			WorkerSummary: batchWorkerSummary("j-impl", api.WorkerSummaryStatusComplete,
				workercompletion.WorkerCompletionReport{LegStatus: "complete"}),
		},
	}
	since := api.UserIntentBoundary(history)
	if workeroutcomes.ImplementationWorkSinceBoundary(history, since) {
		t.Fatal("a writer with no changed paths is not implementation work")
	}
}

func TestBatchReady_verifyNotRequiredAllowsWrapup(t *testing.T) {
	history := inlineEditHistory()
	state := idleBatchReadyState()
	if !workeroutcomes.BatchReadyForSynthesis(state, history, allTerminalProgress(), false, false) {
		t.Fatal("BatchReady must allow wrap-up when verify is not required")
	}
	if workeroutcomes.OpenRepairSinceUserIntent(history, false) {
		t.Fatal("OpenRepair must be false before verify runs")
	}
}

func TestBatchReady_explicitVerifyRequirementWithPass(t *testing.T) {
	history := inlineEditHistory()
	state := idleBatchReadyState()
	if !workeroutcomes.BatchReadyForSynthesis(state, history, allTerminalProgress(), true, true) {
		t.Fatal("BatchReady must be true when host verify passed")
	}
}

func TestOpenRepair_verifyFailed(t *testing.T) {
	history := inlineEditHistory()
	state := idleBatchReadyState()
	if workeroutcomes.BatchReadyForSynthesis(state, history, allTerminalProgress(), true, false) {
		t.Fatal("BatchReady must be false when verify failed")
	}
	if !workeroutcomes.OpenRepairSinceUserIntent(history, true) {
		t.Fatal("OpenRepair must be true for failed host verify")
	}
}

func TestBatchReady_allTerminalPreVerifyInlineEdit(t *testing.T) {
	history := inlineEditHistory()
	state := idleBatchReadyState()
	progress := "- [x] implement\n- [ ] verify"
	if workeroutcomes.BatchReadyForSynthesis(state, history, progress, true, false) {
		t.Fatal("AllTerminal pre-verify with inline edit must block synthesis")
	}
}

func TestBatchReady_planMissingReadOnlyQA(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "what does this repo do?"},
		{Role: api.MessageRoleAssistant, Content: "It is a coding agent."},
	}
	state := idleBatchReadyState()
	if workeroutcomes.BatchReadyForSynthesis(state, history, "", false, false) {
		t.Fatal("BatchReady must be false without a terminal plan")
	}
}

func TestBatchReady_implementerTerminalWithExplicitVerifyRequirement(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "add feature"},
		{
			Role: api.MessageRoleAssistant,
			WorkerSummary: batchWorkerSummary("j-impl", api.WorkerSummaryStatusComplete,
				workercompletion.WorkerCompletionReport{LegStatus: "complete", FilesModified: []string{"src/a.go"}}),
		},
	}
	state := idleBatchReadyState()
	if workeroutcomes.BatchReadyForSynthesis(state, history, allTerminalProgress(), true, false) {
		t.Fatal("writer worker without host verify pass must block synthesis")
	}
}

func TestBatchReady_canceledLegDoesNotWedge(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "add feature"},
		{
			Role: api.MessageRoleAssistant,
			WorkerSummary: batchWorkerSummary("j-cancel", api.WorkerSummaryStatusCanceled,
				workercompletion.WorkerCompletionReport{LegStatus: "partial", Brief: "canceled by coordinator"}),
		},
	}
	state := idleBatchReadyState()
	since := api.UserIntentBoundary(history)
	if workeroutcomes.OpenWorkerEnvelopeSince(history, since) {
		t.Fatal("canceled leg must not register as an open worker envelope")
	}
	if workeroutcomes.OpenRepairSinceUserIntent(history, false) {
		t.Fatal("canceled leg must not be flagged as open repair")
	}
	if !workeroutcomes.BatchReadyForSynthesis(state, history, allTerminalProgress(), false, false) {
		t.Fatal("canceled leg must not wedge BatchReadyForSynthesis")
	}
}

func TestBatchReady_failedLegFlagsRepairWithoutWedgingSynthesis(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "add feature"},
		{
			Role: api.MessageRoleAssistant,
			WorkerSummary: batchWorkerSummary("j-fail", api.WorkerSummaryStatusFailed,
				workercompletion.WorkerCompletionReport{LegStatus: "partial", Brief: "provider rejected the request"}),
		},
	}
	state := idleBatchReadyState()
	since := api.UserIntentBoundary(history)
	if workeroutcomes.OpenWorkerEnvelopeSince(history, since) {
		t.Fatal("failed leg must not register as an open worker envelope")
	}
	if !workeroutcomes.OpenRepairSinceUserIntent(history, false) {
		t.Fatal("failed leg must be flagged as open repair")
	}
	if !workeroutcomes.BatchReadyForSynthesis(state, history, allTerminalProgress(), false, false) {
		t.Fatal("failed leg must not wedge BatchReadyForSynthesis")
	}
}

func TestOpenWorkerEnvelopeSince_blockingStates(t *testing.T) {
	for _, tc := range []struct {
		state      string
		wantRepair bool
	}{
		{state: "open"},
		{state: "needs_decision"},
		{state: "held"},
	} {
		state := tc.state
		status := api.WorkerSummaryStatus(state)
		history := []api.Message{
			{Role: api.MessageRoleUser, Content: "add feature"},
			{
				Role:          api.MessageRoleAssistant,
				WorkerSummary: batchWorkerSummary("j-"+state, status, workercompletion.WorkerCompletionReport{LegStatus: "partial"}),
			},
		}
		since := api.UserIntentBoundary(history)
		if !workeroutcomes.OpenWorkerEnvelopeSince(history, since) {
			t.Fatalf("state %q must register as an open worker envelope", state)
		}
		if got := workeroutcomes.OpenRepairSinceUserIntent(history, false); got != tc.wantRepair {
			t.Fatalf("state %q repair = %v want %v", state, got, tc.wantRepair)
		}
	}
}

func TestOpenWorkerEnvelopeSince_doesNotParseMessageContent(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "add feature"},
		{
			Role:    api.MessageRoleAssistant,
			Content: `<task job_id="j-open" child_session_id="child-j-open" agent_type="implementer" state="open"><summary>working</summary></task>`,
			WorkerSummary: &api.WorkerSummaryMeta{
				WorkerID:       "j-open",
				ChildSessionID: "child-j-open",
				AgentType:      orchestration.ProfileImplementer,
				Status:         api.WorkerSummaryStatusOpen,
			},
		},
	}
	if workeroutcomes.OpenWorkerEnvelopeSince(history, api.UserIntentBoundary(history)) {
		t.Fatal("message content supplied the worker envelope")
	}
}

func TestOpenRepair_partialLegStatus(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "add feature"},
		{
			Role: api.MessageRoleAssistant,
			WorkerSummary: batchWorkerSummary("j-partial", api.WorkerSummaryStatusPartial,
				workercompletion.WorkerCompletionReport{LegStatus: "partial"}),
		},
	}
	if !workeroutcomes.OpenRepairSinceUserIntent(history, false) {
		t.Fatal("partial leg_status must trigger open repair")
	}
}

// A rejected call cannot shift later call-result pairing.
func TestImplementationWorkSinceBoundary_rejectedCallDoesNotDesyncPairing(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "look at the handler"},
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: "c1", Name: "edit", Args: map[string]any{"path": "internal/handler.go"}}},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    "rejected",
			ToolResult: &api.ToolResult{Tool: "edit", ToolCallID: "c1", Outcome: api.ToolResultOutcomeRejected},
		},
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: "c2", Name: "read", Args: map[string]any{"path": "internal/handler.go"}}},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    "package handler",
			ToolResult: &api.ToolResult{Tool: "read", ToolCallID: "c2", Outcome: api.ToolResultOutcomeCompleted},
		},
	}
	since := api.UserIntentBoundary(history)
	if workeroutcomes.ImplementationWorkSinceBoundary(history, since) {
		t.Fatal("a rejected edit plus a completed read is not implementation work")
	}
}

// A mutation after a rejected call still counts.
func TestImplementationWorkSinceBoundary_mutationAfterRejectedCall(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "clean up"},
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: "c1", Name: "read", Args: map[string]any{"path": "a.go"}}},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    "rejected",
			ToolResult: &api.ToolResult{Tool: "read", ToolCallID: "c1", Outcome: api.ToolResultOutcomeRejected},
		},
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: "c2", Name: "delete", Args: map[string]any{"path": "a.go"}}},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    "deleted",
			ToolResult: &api.ToolResult{Tool: "delete", ToolCallID: "c2", Outcome: api.ToolResultOutcomeCompleted},
		},
	}
	since := api.UserIntentBoundary(history)
	if !workeroutcomes.ImplementationWorkSinceBoundary(history, since) {
		t.Fatal("a completed mutation after a rejected call is implementation work")
	}
}
