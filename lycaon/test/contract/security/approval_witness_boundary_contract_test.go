package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
)

// A grant witness pins the attached jail only: socket and boundary-permit
// overlays leave it unchanged, while the jail axes each change it.
func TestApprovalWitnessPinsAttachedJailOnly(t *testing.T) {
	t.Parallel()
	base := hitl.Contained{
		FSJailed: true,
		Egress:   hitl.ContainedEgressProxy,
		Roots:    []string{"/proj"},
	}
	w0 := hitl.BoundaryWitness(base)

	overlay := base
	overlay.SocketPathsDigest = "sock-a"
	overlay.SocketCount = 1
	overlay.BoundaryPermits = []hitl.BoundaryPermit{
		{Kind: hitl.BoundaryPermitUnixSocket, Digest: "sock-a", Count: 1},
		{Kind: hitl.BoundaryPermitLoopback, Digest: "ports-a", Count: 1},
	}
	if w := hitl.BoundaryWitness(overlay); !hitl.WitnessEqual(w, w0) {
		t.Fatalf("overlay changed the witness: %+v vs %+v", w, w0)
	}

	unjailed := base
	unjailed.FSJailed = false
	egress := base
	egress.Egress = hitl.ContainedEgressDeny
	roots := base
	roots.Roots = []string{"/proj", "/other"}
	for name, c := range map[string]hitl.Contained{"fs jail": unjailed, "egress": egress, "roots": roots} {
		if hitl.WitnessEqual(hitl.BoundaryWitness(c), w0) {
			t.Errorf("%s must change the witness", name)
		}
	}
}
