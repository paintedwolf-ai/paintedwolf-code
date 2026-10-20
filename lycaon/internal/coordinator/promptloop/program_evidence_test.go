package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestPortableProgramDoesNotScrapePrintedPathsIntoSourceEvidence(t *testing.T) {
	calls := 0
	loop := NewPromptLoop(PromptLoopDeps{Tools: ToolsDeps{CommitEvidenceToolResult: func(context.Context, string, *api.Session, string, map[string]any, string, string) (string, string, error) {
		calls++
		return "command#1", "scraped legacy body", nil
	}}})
	printed := "cat src/unseen.go\nsrc/unseen.go:42: claimed text"
	portable := api.Message{Content: printed, ToolResult: &api.ToolResult{Content: printed, CommandProgram: &api.CommandProgramReceipt{ID: "program-receipt", ParentReceiptID: "actual-model-parent"}}}
	err := loop.Batch.tagToolHandleOnCommit(t.Context(), "session", &api.Session{ID: "session"}, "command", map[string]any{"command": "external -- fixture-program"}, &portable, true)
	if err != nil {
		t.Fatalf("commit portable result: %v", err)
	}
	if calls != 0 || len(portable.EvidenceHandles) != 0 || portable.Content != printed {
		t.Fatal("portable external stdout populated legacy source evidence")
	}
	legacy := api.Message{Content: printed, ToolResult: &api.ToolResult{Content: printed}}
	err = loop.Batch.tagToolHandleOnCommit(t.Context(), "session", &api.Session{ID: "session"}, "command", map[string]any{"command": "cat src/unseen.go"}, &legacy, true)
	if err != nil {
		t.Fatalf("preserve current restricted command: %v", err)
	}
	if calls != 1 || len(legacy.EvidenceHandles) != 1 {
		t.Fatal("existing restricted command evidence behavior disappeared")
	}
}
