package session

import "github.com/lycaon/lycaon/internal/gate"

// Mint/join keys are the axis. Empty ports on a day/chat lease mean any port.
const (
	listenAxisKey       = "local_listen"
	loopbackAxisKey     = "loopback_connect"
	localNetworkAxisKey = "local_network"
)

func capabilityWideningDecisionWithPosture(p gate.Posture, axes ...string) *gate.Decision {
	_, decision := gate.Evaluate(gate.Facts{
		Stage:              gate.StagePreSpawn,
		Ran:                gate.ProducerApprovalRequest,
		CapabilityWidening: &gate.CapabilityWidening{Axes: axes},
	}, gate.PostureStrict)
	if decision != nil {
		decision.Posture = p
	}
	return decision
}
