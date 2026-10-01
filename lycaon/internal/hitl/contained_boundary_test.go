package hitl_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

func TestGrantKeyVariesWithSocketAndDirectBoundary(t *testing.T) {
	base := hitl.ProposedAction{
		Tool:       "command",
		ProjectDir: "/proj",
		Args:       map[string]any{"command": "true"},
		Contained: hitl.Contained{
			FSJailed: true,
			Egress:   hitl.ContainedEgressProxy,
			Roots:    []string{"/proj"},
		},
	}
	k0 := hitl.GrantKey(base)

	withSockets := base
	withSockets.Contained.SocketPathsDigest = "digest-a"
	withSockets.Contained.SocketCount = 1
	if hitl.GrantKey(withSockets) != k0 {
		t.Fatal("socket overlay changed GrantKey")
	}

	otherSocket := withSockets
	otherSocket.Contained.SocketPathsDigest = "digest-b"
	if hitl.GrantKey(otherSocket) != hitl.GrantKey(withSockets) {
		t.Fatal("socket overlay changed GrantKey")
	}

	direct := base
	direct.Contained.Egress = hitl.ContainedEgressDirectIP
	direct.Contained.DirectIP = true
	if hitl.GrantKey(direct) == k0 {
		t.Fatal("direct IP must change GrantKey")
	}

	// Order-stable roots: same set different order keeps key.
	reordered := base
	reordered.Contained.Roots = []string{"/b", "/a"}
	same := base
	same.Contained.Roots = []string{"/a", "/b"}
	if hitl.GrantKey(reordered) != hitl.GrantKey(same) {
		t.Fatal("root order must not change GrantKey")
	}

	withCapability := base
	withCapability.HostResources = []string{"local-db"}
	if hitl.GrantKey(withCapability) == k0 {
		t.Fatal("resolved host-resource ids must change GrantKey")
	}
	reorderedHostResources := base
	reorderedHostResources.HostResources = []string{"docker", "local-db", "docker"}
	sameHostResources := base
	sameHostResources.HostResources = []string{"local-db", "docker"}
	if hitl.GrantKey(reorderedHostResources) != hitl.GrantKey(sameHostResources) {
		t.Fatal("host-resource identity must be set-stable")
	}

	withFamily := withCapability
	withFamily.HostResourceFamilies = []string{"data-tools"}
	if hitl.GrantKey(withFamily) == hitl.GrantKey(withCapability) {
		t.Fatal("host-resource family must change policy-relevant action identity")
	}

	withTypedPermit := base
	withTypedPermit.Contained.BoundaryPermits = []hitl.BoundaryPermit{{
		Kind: "local_service.named_pipe", Digest: "pipe-a", Count: 1,
	}}
	otherTypedPermit := withTypedPermit
	otherTypedPermit.Contained.BoundaryPermits = []hitl.BoundaryPermit{{
		Kind: "local_service.unix_socket", Digest: "pipe-a", Count: 1,
	}}
	if hitl.GrantKey(withTypedPermit) != hitl.GrantKey(otherTypedPermit) {
		t.Fatal("typed permits changed GrantKey")
	}
}

func TestContainedFromConfinementProjectsSocketAndDirect(t *testing.T) {
	c := &confine.Confinement{
		Roots: []string{"/proj"},
		SocketGrants: []confine.SocketGrant{
			{ApprovedPath: "/tmp/a.sock", ResolvedPath: "/tmp/a.sock"},
		},
		Network: confine.NetworkDirectIP,
	}
	got := hitl.ContainedFromConfinement(c, true)
	if !got.FSJailed || !got.DirectIP || got.Egress != hitl.ContainedEgressDirectIP {
		t.Fatalf("unexpected Contained: %+v", got)
	}
	if !got.LoopbackAccess {
		t.Fatalf("LoopbackAccess must reflect direct localhost authority: %+v", got)
	}
	if got.SocketCount != 1 || got.SocketPathsDigest == "" {
		t.Fatalf("socket projection missing: %+v", got)
	}
	if got.SocketPathsDigest != confine.SocketPathsDigest(c.SocketGrants) {
		t.Fatalf("digest mismatch: %q vs %q", got.SocketPathsDigest, confine.SocketPathsDigest(c.SocketGrants))
	}
	permitCount := 0
	for _, permit := range got.BoundaryPermits {
		permitCount += permit.Count
	}
	if permitCount != 2 {
		t.Fatalf("generalized permits missing: %+v", got.BoundaryPermits)
	}
}

func TestContainedLoopbackAccessMatchesNetworkAuthority(t *testing.T) {
	for _, tc := range []struct {
		name    string
		network confine.NetworkMode
		want    bool
	}{
		{name: "proxy", network: confine.NetworkProxyOnly},
		{name: "deny", network: confine.NetworkDeny},
		{name: "direct", network: confine.NetworkDirectIP, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hitl.ContainedFromConfinement(&confine.Confinement{
				Roots: []string{"/proj"}, Network: tc.network,
			}, true)
			if got.LoopbackAccess != tc.want {
				t.Fatalf("LoopbackAccess=%v want %v", got.LoopbackAccess, tc.want)
			}
		})
	}
}
