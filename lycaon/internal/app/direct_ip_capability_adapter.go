package app

import (
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/tools"
)

type directIPCapabilityAdapter struct {
	rt *approvalstate.DirectIPCapabilityRuntime
}

func (a directIPCapabilityAdapter) IssuePermit(sessionID, toolCallID, actionDigest, requestDigest, confinementDigest string) {
	if a.rt == nil {
		return
	}
	a.rt.IssuePermit(sessionID, toolCallID, actionDigest, requestDigest, confinementDigest)
}

func (a directIPCapabilityAdapter) ConsumePermit(sessionID, toolCallID, actionDigest, requestDigest, confinementDigest string) (bool, error) {
	if a.rt == nil {
		return false, nil
	}
	return a.rt.ConsumePermit(sessionID, toolCallID, actionDigest, requestDigest, confinementDigest)
}

func (a directIPCapabilityAdapter) Authorized(sessionID, toolCallID, actionDigest string) bool {
	if a.rt == nil {
		return false
	}
	return a.rt.Authorized(sessionID, toolCallID, actionDigest)
}

func (a directIPCapabilityAdapter) LeaseCovers(chatSessionID string, lease hitl.DirectIPLease) bool {
	if a.rt == nil {
		return false
	}
	return a.rt.LeaseCovers(chatSessionID, lease)
}

func (a directIPCapabilityAdapter) GrantChat(chatSessionID string, lease hitl.DirectIPLease, grantID, checkpointID string, expiresAt *time.Time) {
	if a.rt == nil {
		return
	}
	a.rt.GrantChat(chatSessionID, lease, grantID, checkpointID, expiresAt)
}

var _ tools.DirectIPCapabilityRuntime = directIPCapabilityAdapter{}
