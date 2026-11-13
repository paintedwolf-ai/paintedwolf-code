package history

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func largeRecordedCommand() []api.Message {
	settled, exit := time.Now(), 1
	return []api.Message{
		{ID: "request", Ord: 2, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Build and verify the game"},
		{ID: "call-row", Ord: 3, Seq: 3, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call", Name: "command", Args: map[string]any{"command": strings.Repeat("large source ", 6000)}}}},
		{ID: "result-row", Ord: 4, Seq: 4, Role: api.MessageRoleTool, EvidenceHandles: []string{"command#1"}, ToolResult: &api.ToolResult{Tool: "command", ToolCallID: "call", Outcome: api.ToolResultOutcomeCompleted,
			Invocation:     &api.InvocationReceipt{ID: "receipt", ToolCallID: "call", Status: "completed", Invoked: true, SourceRevision: "source-revision"},
			CommandProgram: &api.CommandProgramReceipt{ID: "program", ParentReceiptID: "receipt", SourceRef: "output:program-source:program", State: "completed", StatusCode: &exit, SettledAt: &settled}}},
	}
}

func TestCompletedCommandProjectionPreservesFactsAndRecovery(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	rows := largeRecordedCommand()
	got := projectCompletedCommands(t.Context(), rows)
	if len(got) != 2 || len(got[1].ToolCalls) != 0 || got[1].ToolResult != nil {
		t.Fatalf("exchange was not replaced atomically: %+v", got)
	}
	for _, text := range []string{`"exit_code":1`, `"source_revision":"source-revision"`, `"message_id":"call-row"`, `"message_id":"result-row"`, `command#1`, `output:program-source:program`} {
		if !strings.Contains(got[1].Content, text) {
			t.Fatalf("missing %s: %s", text, got[1].Content)
		}
	}
	if len(rows[1].ToolCalls[0].Args["command"].(string)) < 60000 || rows[2].ToolResult.CommandProgram == nil {
		t.Fatal("canonical exchange changed")
	}
	if got := projectCompletedCommands(t.Context(), rows, 100000); len(got) != 3 {
		t.Fatal("fitting exchange projected needlessly")
	}
	rows[2].ToolResult.CommandProgram.SettledAt = nil
	if got := projectCompletedCommands(t.Context(), rows); len(got) != 3 {
		t.Fatal("live program was projected as settled")
	}
}

func TestCommandProjectionRequiresCompleteMatchingReceipts(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	for _, mutate := range []func([]api.Message){
		func(r []api.Message) { r[2].ToolResult.Invocation.ToolCallID = "different" },
		func(r []api.Message) { r[2].ToolResult.CommandProgram.ParentReceiptID = "different" },
		func(r []api.Message) { r[2].ToolResult.ToolCallID = "different" },
		func(r []api.Message) {
			r[1].ToolCalls = append(r[1].ToolCalls, api.ToolCall{ID: "outstanding", Name: "command"})
		},
		func(r []api.Message) { r[1].ToolCalls[0].Name = "other" },
	} {
		rows := largeRecordedCommand()
		mutate(rows)
		if got := projectCompletedCommands(t.Context(), rows); len(got) != 3 {
			t.Fatal("unmatched exchange was projected")
		}
	}
}

func TestFittingLargeCompletedSourceLeavesRoomForContinuation(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	rows := largeRecordedCommand()
	svc := &Service{store: store.NewMemory(), recallAvailable: func(context.Context, *api.Session) bool { return true }}
	cfg := compaction.DefaultCompactionConfig()
	cfg.ModelContextWindow = 8192
	cfg.HardCeilingTokens = 6000
	svc.Compactor = compaction.NewSimpleCompactor(cfg, nil)
	req := modelcall.CompletionRequest{Messages: append([]api.Message{{ID: "standing", Role: api.MessageRoleSystem, Content: strings.Repeat("standing ", 100)}}, rows...)}
	fitted, err := svc.FitRequest(t.Context(), &api.Session{ID: "s"}, rows, req, nil, nil)
	testutil.FailErr(t, "fit settled source", err)
	if modelcall.EstimateRequestInput(fitted) >= 6000 || !strings.Contains(fitted.Messages[len(fitted.Messages)-1].Content, `"exit_code":1`) {
		t.Fatalf("settled source still blocks continuation: %+v", fitted)
	}
	testutil.FailErr(t, "active instruction survives", validateRequestIntent(rows, fitted.Messages))
}
