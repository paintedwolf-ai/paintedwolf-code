package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilitygrants"

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
			action := hitl.ProposedAction{Tool: "command", Args: map[string]any{"invalid": make(chan int)}}
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
