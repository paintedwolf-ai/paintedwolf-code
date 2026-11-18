package gatefixture

import (
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
)

type EmptyDetections struct{}

func (EmptyDetections) MatchAction(hitl.ProposedAction, gate.Posture) (hitl.DetectionMatch, bool) {
	return hitl.DetectionMatch{}, false
}
