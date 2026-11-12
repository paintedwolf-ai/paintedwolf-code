package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/capabilitygrants"
	"slices"

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

func (c *identityCoalescer) Begin(string, string) (toolapproval.ToolApprovalCoalesceBegin, string) {
	c.called = true
	return toolapproval.ToolApprovalCoalesceMint, ""
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
