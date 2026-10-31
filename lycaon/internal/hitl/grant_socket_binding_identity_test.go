package hitl_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/socketref"
)

func TestGrantKeySeparatesSocketObjectAndRealizationIdentity(t *testing.T) {
	base := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "true"}}}
	first := confine.SocketGrant{ApprovedPath: "/run/service.sock", ResolvedPath: "/run/service.sock", Binding: socketref.Binding{Enforcement: socketref.RetainedObject, ID: "first-instance"}}
	base.Sockets.SocketGrants = []confine.SocketGrant{first}
	original := hitl.GrantKey(base)
	for _, binding := range []socketref.Binding{{Enforcement: socketref.RetainedObject, ID: "second-instance"}, {Enforcement: socketref.KernelPathname}, {Enforcement: socketref.NoRealization}} {
		changed := base
		grant := first
		grant.Binding = binding
		changed.Sockets.SocketGrants = []confine.SocketGrant{grant}
		if hitl.GrantKey(changed) == original {
			t.Fatalf("socket authority reused across binding %s", binding.Enforcement)
		}
	}
	if hitl.GrantKey(base) != original {
		t.Fatal("unchanged binding did not retain stable action identity")
	}
}
