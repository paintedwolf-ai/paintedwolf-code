package toolexecution

import (
	"context"
	"slices"

	"github.com/lycaon/lycaon/internal/capabilitygrants"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/session/approvalstate"

	"github.com/lycaon/lycaon/internal/toolapproval"

	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

type identityCheckpointManager struct {
	hitl.CheckpointManager
}

type identityCoalescer struct {
	toolapproval.ToolApprovalCoalesce
	called bool
}

func (c *identityCoalescer) Begin(string, string) (approvalstate.ToolApprovalCoalesceBegin, string) {
	c.called = true
	return approvalstate.ToolApprovalCoalesceMint, ""
}

func TestApprovalRejectsUnencodableIdentityBeforeCoalescing(t *testing.T) {
	for _, coalesceKey := range []string{"", "explicit-key"} {
		t.Run("key="+coalesceKey, func(t *testing.T) {
			coalescer := &identityCoalescer{}
			executor := func() *Executor {
				e := NewExecutor(nil, nil, "")
				e.Approvals.checkpointMgr = identityCheckpointManager{}
				e.Approvals.approvalCoalesce = coalescer
				return e
			}()
			action := hitl.ProposedAction{
				Invocation: hitl.ActionInvocation{
					Tool: "command",
					Args: map[string]any{"invalid": make(chan int)},
				},
			}
			response, err := executor.Approvals.raiseAndWaitToolApproval(t.Context(), toolApprovalRaise{Action: action, CoalesceKey: coalesceKey})
			if err == nil || response != nil || coalescer.called {
				t.Fatalf("unencodable action reached coalescing: response=%+v err=%v coalesced=%t", response, err, coalescer.called)
			}
			permission := &hitl.SecretPermission{}
			if key := permission.Key(hitl.GrantKey(action)); key != "" {
				t.Fatalf("secret suffix made an unusable identity usable: %q", key)
			}
			if key := directIPCoalesceKey(hitl.GrantKey(action), "request"); key != "" {
				t.Fatalf("direct-IP suffix made an unusable identity usable: %q", key)
			}
			if key := capabilitygrants.SocketSetCoalesceKey(action, []confine.SocketGrant{{ApprovedPath: "/socket", ResolvedPath: "/socket"}}); key != "" {
				t.Fatalf("socket suffix made an unusable identity usable: %q", key)
			}
		})
	}
}

func (identityCheckpointManager) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}

// A widening posture adds the command's network right after the host's chat
// permission and keeps every other rung where it was, so the face, the first
// primary chat rung, stays the host.
func TestCommandNetworkSitsBesideTheHostsChatPermission(t *testing.T) {
	once := hitl.ApprovalGrantOffer{ID: "once", Rung: hitl.ApprovalRungOnce}
	host := hitl.ApprovalGrantOffer{ID: "host", Rung: hitl.ApprovalRungChat}
	project := hitl.ApprovalGrantOffer{ID: "project", Rung: hitl.ApprovalRungProject}
	menu := hitl.ApprovalGrantOffer{ID: "menu", Rung: hitl.ApprovalRungChat, Group: hitl.GroupAlsoAllow}
	command := hitl.ApprovalGrantOffer{ID: "command", Rung: hitl.ApprovalRungChat}

	ids := func(offers []hitl.ApprovalGrantOffer) []string {
		out := make([]string, 0, len(offers))
		for _, offer := range offers {
			out = append(out, offer.ID)
		}
		return out
	}
	if got := ids(besideChatSlot([]hitl.ApprovalGrantOffer{once, menu, host, project}, command)); !slices.Equal(got, []string{"once", "menu", "host", "command", "project"}) {
		t.Fatalf("ladder = %v", got)
	}
	if got := ids(besideChatSlot([]hitl.ApprovalGrantOffer{once, project}, command)); !slices.Equal(got, []string{"once", "project", "command"}) {
		t.Fatalf("ladder without a chat slot = %v", got)
	}
}

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
