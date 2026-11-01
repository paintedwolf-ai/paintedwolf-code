package promptresult

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A durable result replayed after a restart carries no offered tools.
func TestOfferedToolsAreNotPersisted(t *testing.T) {
	live := &Result{MessageID: "m1", OfferedTools: []string{"read", "recall"}}
	if !slices.Equal(live.OfferedToolNames(), []string{"read", "recall"}) {
		t.Fatalf("live offered tools = %v", live.OfferedToolNames())
	}
	raw, err := json.Marshal(live)
	testutil.FailErr(t, "encode live result", err)
	if string(raw) != `{"MessageID":"m1"}` {
		t.Fatalf("durable result = %s", raw)
	}
	var replayed *Result
	testutil.FailErr(t, "decode durable result", json.Unmarshal(raw, &replayed))
	if replayed.OfferedToolNames() != nil {
		t.Fatalf("replayed offered tools = %v", replayed.OfferedToolNames())
	}
	var missing *Result
	if missing.OfferedToolNames() != nil {
		t.Fatal("missing result offers tools")
	}
	if missing.IsCloseoutBlocked() {
		t.Fatal("missing result is closeout blocked")
	}
	blocked := &Result{
		MessageID: "m2",
		Outcome:   TurnOutcomeCloseoutBlocked,
		CloseoutBlocked: &CloseoutBlocked{
			Code:           "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT",
			Phase:          "implement",
			OpenGateLeaves: []string{"test"},
			Notice:         "Workflow phase implement is waiting on open gate(s): test (WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT). Send a message or resolve the open gate to continue.",
		},
	}
	if !blocked.IsCloseoutBlocked() {
		t.Fatal("expected closeout blocked outcome")
	}
}
