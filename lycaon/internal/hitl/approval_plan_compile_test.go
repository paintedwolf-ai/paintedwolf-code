package hitl

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSocketApprovalOptionsContinueTheHeldSocketSet(t *testing.T) {
	action := ProposedAction{Tool: "command", Command: "docker version", SessionID: "session-1"}
	req := CheckpointRequest{
		SessionID: "session-1", ToolCallID: "call-1",
		SocketCapability: &SocketCapability{Targets: []SocketCapabilityTarget{{
			ApprovedPath: "/var/run/docker.sock", ResolvedPath: "/var/run/docker.sock",
		}}},
		GrantOffers: []ApprovalGrantOffer{{
			ID: "socket-task", Rung: ApprovalRungChat, Scope: ApprovalGrantScopeChat,
			Title: TitleAllowForThisChat, Coverage: "docker socket",
			ExpiresWhen: ExpiresWhenChatDeleted, ReaskWhen: "the chat is deleted",
			Authority: []ApprovalAuthorityDelta{{Kind: AuthoritySocketChat}},
		}},
	}
	options := socketApprovalOptions(req, action)
	if len(options) != 2 {
		t.Fatalf("options = %d, want once and task", len(options))
	}
	for _, option := range options {
		if option.Authority[0].Kind != AuthoritySocketPermit {
			t.Fatalf("option %q first authority = %q, want socket permit", option.ID, option.Authority[0].Kind)
		}
	}
}

func TestDirectIPApprovalOptionsContinueTheHeldAction(t *testing.T) {
	lease := DirectIPLease{
		ActionDigest: "action", RequestDigest: "request", ConfinementDigest: "confinement",
		CommandSummary: "curl example.com",
	}
	req := CheckpointRequest{
		SessionID: "session-1", ToolCallID: "call-1",
		DirectIPCapability: &DirectIPCapability{ActionDigest: lease.ActionDigest},
		GrantOffers: []ApprovalGrantOffer{{
			ID: "direct-task", Rung: ApprovalRungChat, Scope: ApprovalGrantScopeChat,
			Title: TitleAllowForThisChat, Coverage: "direct network",
			ExpiresWhen: ExpiresWhenChatDeleted, ReaskWhen: "the chat is deleted",
			Authority: []ApprovalAuthorityDelta{{Kind: AuthorityDirectIPChat, DirectIPLease: &lease}},
		}},
	}
	options, err := directIPApprovalOptions(req)
	testutil.FailErr(t, "directIPApprovalOptions", err)
	if len(options) != 2 {
		t.Fatalf("options = %d, want once and task", len(options))
	}
	for _, option := range options {
		if option.Authority[0].Kind != AuthorityDirectIPPermit {
			t.Fatalf("option %q first authority = %q, want direct-IP permit", option.ID, option.Authority[0].Kind)
		}
	}
}
