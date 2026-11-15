package toolexecution

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
)

func repeatRaise(reasonKey, subject string) toolApprovalRaise {
	return toolApprovalRaise{
		Decision: &gate.Decision{ReasonKey: reasonKey},
		Plan:     &hitl.ApprovalPlan{Subject: hitl.ApprovalSubject{Targets: []hitl.ApprovalTarget{{Label: subject}}}},
	}
}

// Asking again about the same subject escalates as a repeat, as asking about a new subject does.
func TestRepeatedAsksEscalateForTheSameAndDistinctSubjects(t *testing.T) {
	approvals := &Approvals{}
	approvals.SetGateRepeatLedger(approvalstate.NewGateRepeatLedger())

	if repeat := approvals.noteRepeatAsk("chat", repeatRaise("outside_root", "notes.txt")); repeat != nil {
		t.Fatalf("first ask = %+v, want no repeat context", repeat)
	}
	repeat := approvals.noteRepeatAsk("chat", repeatRaise("outside_root", "notes.txt"))
	if repeat == nil || repeat.Asks != 2 || repeat.Count != 1 {
		t.Fatalf("second ask on one subject = %+v, want asks 2 over 1 subject", repeat)
	}
	repeat = approvals.noteRepeatAsk("chat", repeatRaise("outside_root", "plan.txt"))
	if repeat == nil || repeat.Asks != 3 || repeat.Count != 2 || len(repeat.Subjects) != 1 || repeat.Subjects[0] != "notes.txt" {
		t.Fatalf("ask on a new subject = %+v, want asks 3 over 2 subjects naming the earlier one", repeat)
	}
	if repeat := approvals.noteRepeatAsk("other-chat", repeatRaise("outside_root", "notes.txt")); repeat != nil {
		t.Fatalf("first ask in another chat = %+v, want no repeat context", repeat)
	}
}
