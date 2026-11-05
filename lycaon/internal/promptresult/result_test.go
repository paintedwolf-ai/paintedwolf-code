package promptresult

import (
	"github.com/lycaon/lycaon/internal/toolreach"
	"encoding/json"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A durable result replayed after a restart carries no offered tools.
func TestOfferedToolsAreNotPersisted(t *testing.T) {
	live := &Result{MessageID: "m1", Reach: toolreach.Offered([]string{"read", "recall"})}
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
		Outcome:   OutcomeCloseoutBlocked,
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

func TestTerminalOutcomeDiscriminantSurvivesIrrelevantPayloads(t *testing.T) {
	for _, outcome := range []Outcome{OutcomeCompleted, OutcomeCloseoutBlocked, OutcomeOutputTruncated} {
		for _, details := range []bool{false, true} {
			result := &Result{MessageID: "m1", Outcome: outcome}
			if details {
				result.CloseoutBlocked = &CloseoutBlocked{Code: "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT"}
				result.OutputTruncated = &OutputTruncatedOutcome{ProviderID: "fixture"}
			}
			if result.IsCloseoutBlocked() != (outcome == OutcomeCloseoutBlocked) {
				t.Fatalf("payload changed the outcome: %+v", result)
			}
			raw, err := json.Marshal(result)
			testutil.FailErr(t, "encode terminal outcome", err)
			var restored Result
			testutil.FailErr(t, "restore terminal outcome", json.Unmarshal(raw, &restored))
			if restored.MessageID != result.MessageID || restored.Outcome != outcome || restored.IsCloseoutBlocked() != result.IsCloseoutBlocked() {
				t.Fatalf("durable replay changed terminal meaning: %s", raw)
			}
		}
	}
}
