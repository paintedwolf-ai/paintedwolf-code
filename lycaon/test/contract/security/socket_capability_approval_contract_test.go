package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/capabilitygrants"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

// The socket execution card may widen how long an answer lasts, never whether
// it expires: every offer is task-bound or time-boxed and none is
// device-scoped, so no answer to "connect to this daemon" outlives review.
func TestContractSocketCapabilityCardScopes(t *testing.T) {
	t.Parallel()
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
		},
		Scope: hitl.ActionScope{
			SessionID:  "sess",
			ProjectID:  "proj",
			ProjectDir: "/tmp/proj",
		},
	}
	grant := confine.SocketGrant{ApprovedPath: "/tmp/svc.sock", ResolvedPath: "/private/tmp/svc.sock"}
	offers := capabilitygrants.SocketExecutionGrantOffers(action, []confine.SocketGrant{grant})
	if len(offers) == 0 {
		t.Fatal("socket execution card offered no ladder")
	}
	for _, offer := range offers {
		if offer.Scope == hitl.ApprovalGrantScopeDevice {
			t.Fatalf("socket execution card offered device scope: %+v", offer)
		}
		if offer.Scope != hitl.ApprovalGrantScopeChat && offer.TTLSeconds <= 0 && offer.Grant.ExpiresAt == nil {
			t.Fatalf("socket execution card offered a standing grant beyond the task: %+v", offer)
		}
	}
}
