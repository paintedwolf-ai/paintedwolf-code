package sessions

import (
	"context"
	"net/netip"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/session"
)

// ownedLoopbackResolver rechecks current policy and the exact native listener.
// Confinement also requires this action's narrowed conditional admission; this
// observation never supplies an independent or persistent connect grant.
func ownedLoopbackResolver(provenance session.LoopbackProvenanceResolver, posture func(string) gate.Posture) confine.OwnedLoopbackResolver {
	return func(ctx context.Context, command confine.EgressCommand, endpoint netip.AddrPort) (string, bool) {
		current := gate.DefaultPosture
		if posture != nil {
			current = posture(command.ProjectDir)
		}
		verdict, _ := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest, CapabilityWidening: &gate.CapabilityWidening{Axes: []string{gate.AxisLoopbackConnect}, Ports: []uint16{endpoint.Port()}, SelfSpawned: true}}, current)
		if verdict == gate.Ask {
			return "", false
		}
		return session.ObserveOwnedSessionListener(ctx, provenance, command.SessionID, endpoint)
	}
}
