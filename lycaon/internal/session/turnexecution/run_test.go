package turnexecution

import (
	"github.com/lycaon/lycaon/internal/toolreach"
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/turnsettlement"
)

type recordingGuidance struct {
	emitted []anchor.ID
}

func (g *recordingGuidance) PendingIDs(context.Context, string) []string { return nil }
func (g *recordingGuidance) Emit(_ context.Context, _ string, id anchor.ID, _ anchor.Envelope) {
	g.emitted = append(g.emitted, id)
}

func TestRecordPromptOutcomeNotesCloseoutBlockedAndEmitsGateBlocked(t *testing.T) {
	guidance := &recordingGuidance{}
	settlement := turnsettlement.New(nil, nil, nil, nil, nil, nil)
	svc := &Service{Guidance: guidance, Settlement: settlement}

	blocked := &promptresult.CloseoutBlocked{
		Code:           "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT",
		Phase:          "implement",
		OpenGateLeaves: []string{"test"},
		Notice:         "Workflow phase implement is waiting on open gate(s): test (WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT).",
	}
	res := svc.recordPromptOutcome(t.Context(), "sess-1", &promptloop.PromptRunResult{
		LastAssistantID: "notice-1",
		Reach:           toolreach.Offered([]string{"read"}),
		Outcome:         promptresult.OutcomeCloseoutBlocked,
		CloseoutBlocked: blocked,
	})
	if res == nil || !res.IsCloseoutBlocked() || res.CloseoutBlocked != blocked || res.MessageID != "notice-1" {
		t.Fatalf("unexpected outcome result: %+v", res)
	}
	if len(guidance.emitted) != 1 || guidance.emitted[0] != anchor.GateBlocked {
		t.Fatalf("expected GateBlocked kick, got %v", guidance.emitted)
	}
}
