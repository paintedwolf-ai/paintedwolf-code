package settings

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

func TestSocketContainmentSubtractsOnlyCoveredBoundaryMembers(t *testing.T) {
	one := confine.SocketGrant{ApprovedPath: "/run/one.sock", ResolvedPath: "/run/one.sock"}
	two := confine.SocketGrant{ApprovedPath: "/run/two.sock", ResolvedPath: "/run/two.sock"}
	grants := []confine.SocketGrant{one, two}
	oneDigest := confine.SocketPathsDigest(grants[:1])
	twoDigest := confine.SocketPathsDigest(grants[1:])
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Sockets: hitl.ActionSockets{
SocketGrants: grants,
},
Execution: hitl.ActionExecution{
Contained: hitl.Contained{SocketCount: 2, SocketPathsDigest: confine.SocketPathsDigest(grants)},
},
}
	for _, tc := range []struct {
		name    string
		digests []string
		want    int
	}{
		{"none", nil, 2}, {"one", []string{oneDigest}, 1},
		{"both", []string{oneDigest, twoDigest}, 0},
		{"duplicate is not two grants", []string{oneDigest, oneDigest}, 1},
		{"unrelated", []string{"unknown"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action.Sockets.AuthorizedSocketDigests = tc.digests
			if got := containmentFor(action); got.SocketCount != tc.want {
				t.Fatalf("uncovered sockets=%d, want %d", got.SocketCount, tc.want)
			}
		})
	}
	action.Sockets.AuthorizedSocketDigests = []string{oneDigest, twoDigest}
	action.Execution.Contained.SocketPathsDigest = "different-boundary"
	if got := containmentFor(action); got.SocketCount != 2 {
		t.Fatal("authorization covered a different applied boundary")
	}
}
