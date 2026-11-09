package compaction

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestContinuationRetainsFailedExecutionWithoutSummary(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	code := 1
	result := &api.ToolResult{Tool: "command", ToolCallID: "call", Outcome: api.ToolResultOutcomeCompleted,
		Invocation:     &api.InvocationReceipt{ID: "receipt", ToolCallID: "call", Invoked: true, Status: "completed", SourceRevision: "revision", SourceVerdict: "failed"},
		CommandProgram: &api.CommandProgramReceipt{ID: "program", ParentReceiptID: "receipt", State: "completed", StatusCode: &code},
		Process:        &api.ToolProcessHandle{Handle: "process"}}
	row := ContextMessageFromAPI(api.Message{ID: "result", Ord: 5, Role: api.MessageRoleTool, Content: "failed build", ToolResult: result})
	sources, _, err := continuationSegments([]ContextMessage{row}, 64)
	testutil.FailErr(t, "project execution", err)
	if len(sources) != 1 || sources[0].Execution == nil || sources[0].Execution.ExitCode == nil || *sources[0].Execution.ExitCode != 1 {
		t.Fatalf("execution lost: %+v", sources)
	}
	record := &messageview.Continuation{Format: messageview.ContinuationFormat, Window: 32768, ThroughOrd: 5, Sources: sources}
	clone := messageview.CloneContinuation(record)
	*clone.Sources[0].Execution.ExitCode = 0
	if *record.Sources[0].Execution.ExitCode != 1 {
		t.Fatal("cloning changed recorded exit")
	}
	rendered, err := RenderContinuation(t.Context(), record, true, 0)
	testutil.FailErr(t, "render execution without prose", err)
	for _, want := range []string{`"exit_code":1`, `"receipt_id":"receipt"`, `"process_handle":"process"`, `"source_verdict":"failed"`} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %s in %s", want, rendered)
		}
	}
}

func TestSummaryFragmentsKeepLinkageAndHandles(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	row := ContextMessage{ID: "write-message", Ord: 4, Role: "assistant", ToolCalls: []api.ToolCall{{ID: "call", Name: "command", Args: map[string]any{"command": strings.Repeat("source", 200)}}}, EvidenceHandles: []string{"command#1"}}
	_, fragments, err := continuationSegments([]ContextMessage{row}, 32)
	testutil.FailErr(t, "split source", err)
	if len(fragments) < 2 {
		t.Fatal("fixture did not split")
	}
	for _, fragment := range fragments {
		var data map[string]any
		testutil.FailErr(t, "decode fragment", json.Unmarshal([]byte(fragment.Content), &data))
		if data["message_id"] != "write-message" || data["calls"] == nil {
			t.Fatalf("fragment lost identity: %+v", data)
		}
	}
	rows, _, err := compactionRecentMessages(fragments[:1])
	testutil.FailErr(t, "prepare fragment", err)
	rendered, err := renderCompactionUserPrompt(t.Context(), SessionInfo{}, rows)
	testutil.FailErr(t, "render handles", err)
	if !strings.Contains(rendered, "command#1") || strings.Contains(rendered, "interface {} Value") {
		t.Fatalf("unusable handles: %s", rendered)
	}
}
