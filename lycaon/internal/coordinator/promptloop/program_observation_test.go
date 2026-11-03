package promptloop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestProgramObservationTagsOutcomeAlongsideDeliveredSource(t *testing.T) {
	for _, content := range []string{"tests passed", "tests failed", "program running"} {
		calls := 0
		loop := NewPromptLoop(PromptLoopDeps{Tools: ToolsDeps{
			CommitProgramObservation: func(_ context.Context, session, tool string, receipt *api.CommandProgramReceipt, body string) (string, error) {
				calls++
				if session != "session" || tool != "command" || receipt.ID != "program" || body != content {
					t.Fatalf("wrong observation: %s %s %+v %q", session, tool, receipt, body)
				}
				return "command#1", nil
			},
			CommitEvidenceToolResult: func(context.Context, string, *api.Session, string, map[string]any, string, string) (string, string, error) {
				t.Fatal("program output reached source scraper")
				return "", "", nil
			},
		}})
		msg := api.Message{Content: content, EvidenceHandles: []string{"read#1"}, ToolResult: &api.ToolResult{Content: content, CommandProgram: &api.CommandProgramReceipt{ID: "program", ParentReceiptID: "parent"}}}
		if err := loop.Batch.tagToolHandleOnCommit(t.Context(), "session", &api.Session{ID: "session"}, "command", nil, &msg, false); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || len(msg.EvidenceHandles) != 2 || !strings.Contains(msg.Content, "command#1") || !strings.Contains(msg.Content, "read#1") || msg.ToolResult.Content != msg.Content {
			t.Fatalf("lost observed handles: %+v", msg)
		}
	}
}

func TestProgramObservationCommitErrorPreventsDelivery(t *testing.T) {
	failed := errors.New("evidence unavailable")
	loop := NewPromptLoop(PromptLoopDeps{Tools: ToolsDeps{CommitProgramObservation: func(context.Context, string, string, *api.CommandProgramReceipt, string) (string, error) {
		return "", failed
	}}})
	msg := api.Message{ToolResult: &api.ToolResult{CommandProgram: &api.CommandProgramReceipt{ID: "program", ParentReceiptID: "parent"}}}
	if err := loop.Batch.tagToolHandleOnCommit(t.Context(), "session", nil, "command", nil, &msg, true); !errors.Is(err, failed) {
		t.Fatalf("commit error lost: %v", err)
	}
}
