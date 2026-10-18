package transcript

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func progressRecoveryFixture() []api.Message {
	return []api.Message{
		{ID: "request", Role: api.MessageRoleUser, Content: "Create the requested files"},
		{ID: "rejected-write", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Tool: "write", ToolCallID: "write-call", ToolArgs: map[string]any{"path": "temperature.py", "content": "print('hello')"},
			Invocation: &api.InvocationReceipt{ID: "write-receipt", Tool: "write", ToolCallID: "write-call", Status: api.InvocationStatusRejected, Failure: &api.InvocationFailure{Code: "PROGRESS_MISSING"}},
		}},
		{ID: "progress-result", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Tool: "update_progress", ToolCallID: "progress-call", Content: `{"status":"updated"}`,
			Invocation: &api.InvocationReceipt{ID: "progress-receipt", Tool: "update_progress", ToolCallID: "progress-call", Status: api.InvocationStatusCompleted, Invoked: true},
		}},
	}
}

func TestProgressRepairKeepsUnexecutedCallVisible(t *testing.T) {
	messages := progressRecoveryFixture()
	projected := Project(messages)
	got := projected[len(projected)-1].Content
	for _, want := range []string{"prior_unexecuted_calls", "rejected-write", "write-call", "temperature.py"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress result lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "print('hello')") {
		t.Fatal("reminder duplicated file contents")
	}
	if strings.Contains(messages[2].ToolResult.Content, "prior_unexecuted_calls") {
		t.Fatal("projection modified durable result")
	}
	again := Project(projected)
	if again[len(again)-1].Content != got {
		t.Fatal("projection is not idempotent")
	}
}

func TestProgressRepairRequiresPairedUnexecutedReceipts(t *testing.T) {
	cases := map[string]func([]api.Message){
		"already invoked":         func(m []api.Message) { m[1].ToolResult.Invocation.Invoked = true },
		"different rejection":     func(m []api.Message) { m[1].ToolResult.Invocation.Failure.Code = "APPROVAL_DENIED" },
		"missing failure":         func(m []api.Message) { m[1].ToolResult.Invocation.Failure = nil },
		"missing receipt":         func(m []api.Message) { m[1].ToolResult.Invocation = nil },
		"unpaired call":           func(m []api.Message) { m[1].ToolResult.Invocation.ToolCallID = "other" },
		"unpaired tool":           func(m []api.Message) { m[1].ToolResult.Invocation.Tool = "edit" },
		"missing result identity": func(m []api.Message) { m[1].ID = "" },
		"assistant spoof":         func(m []api.Message) { m[1].Role = api.MessageRoleAssistant },
		"progress failed":         func(m []api.Message) { m[2].ToolResult.Invocation.Status = api.InvocationStatusError },
		"progress unchanged":      func(m []api.Message) { m[2].ToolResult.Content = `{"status":"unchanged"}` },
		"progress malformed":      func(m []api.Message) { m[2].ToolResult.Content = `updated` },
		"progress not invoked":    func(m []api.Message) { m[2].ToolResult.Invocation.Invoked = false },
	}
	for name, modify := range cases {
		t.Run(name, func(t *testing.T) {
			m := progressRecoveryFixture()
			modify(m)
			if got := progressRecoveryParts(m[:2], m[2]); len(got) != 0 {
				t.Fatalf("unexpected reminder: %+v", got)
			}
		})
	}
}

func TestProgressRepairStopsAtNewRequestOrLaterAttempt(t *testing.T) {
	for _, kind := range []string{"user", "progress", "write", "old"} {
		t.Run(kind, func(t *testing.T) {
			m := progressRecoveryFixture()
			history := append([]api.Message(nil), m[:2]...)
			switch kind {
			case "user":
				history = append(history, api.Message{Role: api.MessageRoleUser, Content: "Stop that task"})
			case "progress":
				history = append(history, m[2])
			case "write":
				later := *m[1].ToolResult
				receipt := *later.Invocation
				receipt.Status = api.InvocationStatusCompleted
				receipt.Invoked = true
				later.Invocation = &receipt
				history = append(history, api.Message{Role: api.MessageRoleTool, ToolResult: &later})
			case "old":
				history = append(history, make([]api.Message, 32)...)
			}
			if got := progressRecoveryParts(history, m[2]); len(got) != 0 {
				t.Fatalf("stale reminder: %+v", got)
			}
		})
	}
}

func TestProgressRepairBoundsDataAndKeepsAuthority(t *testing.T) {
	m := progressRecoveryFixture()
	m[1].ToolResult.ToolArgs["path"] = strings.Repeat("x", 257)
	parts := progressRecoveryParts(m[:2], m[2])
	if len(parts) != 1 || strings.Contains(parts[0].Content, strings.Repeat("x", 257)) {
		t.Fatalf("unbounded path: %+v", parts)
	}
	if parts[0].Authority != api.ContentAuthorityNone || parts[0].Origin != api.MessageOriginHost {
		t.Fatal("receipt gained instruction authority")
	}
	m[1].ID = strings.Repeat("x", 2049)
	if len(progressRecoveryParts(m[:2], m[2])) != 0 {
		t.Fatal("oversized reminder emitted")
	}
}
