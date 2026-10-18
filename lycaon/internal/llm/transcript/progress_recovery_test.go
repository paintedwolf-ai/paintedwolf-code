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
	parts := ProgressRecovery(messages)
	if len(parts) != 1 {
		t.Fatalf("missing recovery data: %+v", parts)
	}
	got := parts[0].Content
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
	again := ProgressRecovery(messages)
	if again[0].Content != got {
		t.Fatal("recovery projection changed")
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

func TestProgressRepairGuidanceTracksLatestResult(t *testing.T) {
	m := progressRecoveryFixture()
	if len(ProgressRecovery(m)) == 0 {
		t.Fatal("fresh repair missing")
	}
	internal := api.Message{Role: api.MessageRoleUser, Visibility: api.MessageVisibilityInternal, Origin: api.MessageOriginHost, Content: "workflow facts"}
	if len(ProgressRecovery(append(m, internal))) == 0 {
		t.Fatal("internal host frame hid repair")
	}
	if len(ProgressRecovery(append(m, api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "read"}}))) != 0 {
		t.Fatal("later result retained repair guidance")
	}
	if len(ProgressRecovery(append(m, api.Message{Role: api.MessageRoleUser, Content: "stop"}))) != 0 {
		t.Fatal("new user request retained repair guidance")
	}
	if len(ProgressRecovery(nil)) != 0 {
		t.Fatal("empty history claims repair")
	}
}

func TestProgressRepairRetainsDistinctTargetsInOrder(t *testing.T) {
	m := progressRecoveryFixture()
	second := *m[1].ToolResult
	receipt := *second.Invocation
	receipt.ID = "readme-receipt"
	receipt.ToolCallID = "readme-call"
	second.Invocation = &receipt
	second.ToolCallID = "readme-call"
	second.ToolArgs = map[string]any{"path": "README.md"}
	history := append([]api.Message(nil), m[:2]...)
	history = append(history, api.Message{ID: "readme-result", Role: api.MessageRoleTool, ToolResult: &second})
	parts := progressRecoveryParts(history, m[2])
	if len(parts) != 1 {
		t.Fatalf("missing reminder: %+v", parts)
	}
	text := parts[0].Content
	if strings.Index(text, "temperature.py") < 0 || strings.Index(text, "README.md") < strings.Index(text, "temperature.py") {
		t.Fatalf("distinct calls lost or reordered: %s", text)
	}
}
