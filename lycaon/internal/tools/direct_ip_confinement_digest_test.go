package tools

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
)

func TestDirectIPConfinementDigestIgnoresSocketOverlay(t *testing.T) {
	t.Parallel()
	base := hitl.Contained{
		FSJailed: true,
		Egress:   hitl.ContainedEgressDirectIP,
		Roots:    []string{"/tmp/proj"},
		DirectIP: true,
	}
	plain := directIPConfinementDigest(base)
	withSockets := base
	withSockets.SocketPathsDigest = "sock-a"
	withSockets.SocketCount = 1
	if directIPConfinementDigest(withSockets) != plain {
		t.Fatal("socket overlay changed the direct-IP confinement digest")
	}

	shrunk := base
	shrunk.Roots = []string{"/tmp/other"}
	if directIPConfinementDigest(shrunk) == plain {
		t.Fatal("attached-root change must move the direct-IP confinement digest")
	}
}

func TestDirectIPConfinementDigestKeepsTransportNarrowing(t *testing.T) {
	t.Parallel()
	wide := hitl.Contained{
		FSJailed: true,
		Egress:   hitl.ContainedEgressDirectIP,
		Roots:    []string{"/tmp/proj"},
		DirectIP: true,
		BoundaryPermits: []hitl.BoundaryPermit{{
			Kind: hitl.BoundaryPermitDirectIP, Digest: "enabled", Count: 1,
		}},
	}
	narrow := wide
	narrow.BoundaryPermits = []hitl.BoundaryPermit{{
		Kind: hitl.BoundaryPermitDirectIP, Digest: "udp-123", Count: 1,
	}}
	if directIPConfinementDigest(wide) == directIPConfinementDigest(narrow) {
		t.Fatal("UDP/123 and the open box must not share a confinement digest")
	}
}
