package hitl_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
)

func TestBoundaryWitnessIgnoresCapabilityOverlays(t *testing.T) {
	t.Parallel()
	base := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/b", "/a"}}
	plain := hitl.BoundaryWitness(base)
	if plain.RootsDigest == "" {
		t.Fatal("attached roots must produce a digest")
	}

	reordered := base
	reordered.Roots = []string{"/a", "/b"}
	if !hitl.WitnessEqual(plain, hitl.BoundaryWitness(reordered)) {
		t.Fatal("root order must not change the witness")
	}

	overlay := base
	overlay.SocketPathsDigest = "sock"
	overlay.SocketCount = 2
	overlay.DirectIP = true
	overlay.BoundaryPermits = []hitl.BoundaryPermit{{
		Kind: hitl.BoundaryPermitUnixSocket, Digest: "sock", Count: 2,
	}}
	if !hitl.WitnessEqual(plain, hitl.BoundaryWitness(overlay)) {
		t.Fatal("capability overlay changed BoundaryWitness")
	}

	if hitl.RootsDigest([]string{"/a", " /b "}) != hitl.RootsDigest([]string{"/b", "/a"}) {
		t.Fatal("RootsDigest must trim and order-stabilize")
	}
}
