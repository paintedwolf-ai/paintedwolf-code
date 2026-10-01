package harnessfixture

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type preludeTranscript struct{ rows []api.Message }

func (s *preludeTranscript) GetMessages(context.Context, string) ([]api.Message, error) {
	return s.rows, nil
}

func TestPreludeUsesDurableToolIdentityAndSurvivesRestart(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	rows := &preludeTranscript{}
	root, session := t.TempDir(), uuid.NewString()
	c, err := NewPreludeController(root, rows)
	testutil.FailErr(t, "create preparation", err)
	plan := Prelude{OperationID: uuid.NewString(), Steps: []PreludeStep{{ID: "baseline", Tool: "verify", Args: map[string]any{"command": "python3 -B check.py"}, Outcome: api.ToolResultOutcomeCompleted, JSON: map[string]any{"outcome": "passed"}}}}
	testutil.FailErr(t, "install plan", c.Install(t.Context(), session, plan))
	request := modelcall.CompletionRequest{Debug: modelcall.RequestDebug{SessionID: session}}
	first, err := c.next(t.Context(), request)
	testutil.FailErr(t, "first setup call", err)
	if first == nil || first.ID != plan.CallID(plan.Steps[0]) {
		t.Fatalf("setup call: %+v", first)
	}
	// An unrelated result, even with the right content, cannot advance setup.
	rows.rows = []api.Message{{Seq: 1, ToolResult: &api.ToolResult{ToolCallID: uuid.NewString(), Outcome: api.ToolResultOutcomeCompleted, Content: `{"outcome":"passed"}`}}}
	again, err := c.next(t.Context(), request)
	testutil.FailErr(t, "ignore unrelated result", err)
	if again == nil || again.ID != first.ID {
		t.Fatal("unrelated evidence advanced preparation")
	}
	rows.rows = append(rows.rows, api.Message{Seq: 2, ToolCalls: []api.ToolCall{*first}})
	if _, err := c.next(t.Context(), request); err == nil {
		t.Fatal("ambiguous in-flight invocation was replayed")
	}
	rows.rows = append(rows.rows, api.Message{Seq: 3, ToolResult: &api.ToolResult{ToolCallID: first.ID, Outcome: api.ToolResultOutcomeCompleted, Content: `{"outcome":"passed"}`}})
	restarted, err := NewPreludeController(root, rows)
	testutil.FailErr(t, "restart preparation", err)
	testutil.FailErr(t, "reinstall identical plan", restarted.Install(t.Context(), session, plan))
	next, err := restarted.next(t.Context(), request)
	testutil.FailErr(t, "enter candidate", err)
	if next != nil {
		t.Fatal("completed setup replayed after restart")
	}
	receipt, err := restarted.Receipt(session)
	testutil.FailErr(t, "read entry receipt", err)
	if receipt.TranscriptSeq != 3 || len(receipt.ToolCallIDs) != 1 || receipt.EntryAt.IsZero() {
		t.Fatalf("entry receipt: %+v", receipt)
	}
	plan.Steps[0].Tool = "command"
	if err := restarted.Install(t.Context(), session, plan); err == nil {
		t.Fatal("changed setup accepted under old identity")
	}
}

func TestPreludeRejectsWrongBoundaryFacts(t *testing.T) {
	step := PreludeStep{ID: "check", Outcome: api.ToolResultOutcomeCompleted, JSON: map[string]any{"outcome": "passed"}}
	for _, result := range []api.ToolResult{
		{Outcome: api.ToolResultOutcomeRejected, Content: `{"outcome":"passed"}`},
		{Outcome: api.ToolResultOutcomeCompleted, Content: `{"outcome":"failed"}`},
		{Outcome: api.ToolResultOutcomeCompleted, Content: `This check passed`},
		{Outcome: api.ToolResultOutcomeCompleted, Content: `{"outcome":true}`},
	} {
		if err := matchPreludeResult(step, &result); err == nil {
			t.Fatalf("accepted wrong boundary: %+v", result)
		}
	}
	step.JSON = nil
	step.Outcome = api.ToolResultOutcomeRejected
	step.Code = "ACCESS_DECLINED"
	testutil.FailErr(t, "typed refusal", matchPreludeResult(step, &api.ToolResult{Outcome: step.Outcome, Codes: []string{"ACCESS_DECLINED"}}))
	if err := matchPreludeResult(step, &api.ToolResult{Outcome: step.Outcome, Content: "ACCESS_DECLINED"}); err == nil {
		t.Fatal("prose granted result authority")
	}
}

func TestPreludeUnavailableOutsideHarness(t *testing.T) {
	t.Setenv(configdir.EnvHarness, "0")
	if _, err := NewPreludeController(t.TempDir(), &preludeTranscript{}); err == nil {
		t.Fatal("production preparation enabled")
	}
}

func TestPreludeInitialWorkflowAndRealFollowUpBoundary(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	rows := &preludeTranscript{rows: []api.Message{{Role: api.MessageRoleSystem}}}
	controller, err := NewPreludeController(t.TempDir(), rows)
	testutil.FailErr(t, "create preparation", err)
	session := uuid.NewString()
	plan := Prelude{OperationID: uuid.NewString(), Final: "The original check passed.", Steps: []PreludeStep{{ID: "baseline", Tool: "verify", Args: map[string]any{"count": 1}, Outcome: api.ToolResultOutcomeCompleted, JSON: map[string]any{"count": 1}}}}
	testutil.FailErr(t, "install after workflow initialization", controller.Install(t.Context(), session, plan))
	testutil.FailErr(t, "idempotent numeric plan", controller.Install(t.Context(), session, plan))
	rows.rows = append(rows.rows, api.Message{Role: api.MessageRoleUser}, api.Message{ToolResult: &api.ToolResult{ToolCallID: plan.CallID(plan.Steps[0]), Outcome: api.ToolResultOutcomeCompleted, Content: `{"count":1}`}})
	request := modelcall.CompletionRequest{Debug: modelcall.RequestDebug{SessionID: session}}
	_, err = controller.next(t.Context(), request)
	testutil.FailErr(t, "finish preparation tool", err)
	if _, err := controller.Receipt(session); err == nil {
		t.Fatal("candidate entered before changed user intent")
	}
	final, err := controller.priorHandoff(t.Context(), session)
	testutil.FailErr(t, "initial handoff", err)
	if final != plan.Final {
		t.Fatalf("initial handoff = %q", final)
	}
	rows.rows = append(rows.rows, api.Message{Role: api.MessageRoleUser, Visibility: api.MessageVisibilityInternal})
	_, err = controller.next(t.Context(), request)
	testutil.FailErr(t, "host message is not changed intent", err)
	if _, err := controller.Receipt(session); err == nil {
		t.Fatal("internal user message started candidate")
	}
	rows.rows = append(rows.rows, api.Message{Role: api.MessageRoleUser, Seq: 8})
	_, err = controller.next(t.Context(), request)
	testutil.FailErr(t, "enter after follow-up", err)
	entry, err := controller.Receipt(session)
	testutil.FailErr(t, "read candidate entry", err)
	if entry.TranscriptSeq != 8 {
		t.Fatalf("entry transcript seq = %d", entry.TranscriptSeq)
	}
}

func TestPreludeSessionValidation(t *testing.T) {
	controller := &PreludeController{}
	for _, sessionID := range []string{"", "invalid-session"} {
		request := modelcall.CompletionRequest{Debug: modelcall.RequestDebug{SessionID: sessionID}}
		_, nextErr := controller.next(t.Context(), request)
		_, handoffErr := controller.priorHandoff(t.Context(), sessionID)
		wantError := sessionID != ""
		if (nextErr != nil) != wantError || (handoffErr != nil) != wantError {
			t.Fatalf("session %q: next=%v handoff=%v", sessionID, nextErr, handoffErr)
		}
	}
}
