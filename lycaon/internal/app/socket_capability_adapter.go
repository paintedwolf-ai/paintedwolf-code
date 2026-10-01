package app

import (
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/tools"
)

type socketCapabilityAdapter struct {
	rt *approvalstate.SocketCapabilityRuntime
}

func (a socketCapabilityAdapter) AppliedGrants(rootSessionID string) []confine.SocketGrant {
	if a.rt == nil {
		return nil
	}
	return a.rt.AppliedGrants(rootSessionID)
}

func (a socketCapabilityAdapter) AuthorizedGrants(rootSessionID, sessionID, toolCallID, actionDigest string, requested []confine.SocketGrant) []confine.SocketGrant {
	if a.rt == nil {
		return nil
	}
	return a.rt.AuthorizedGrants(rootSessionID, sessionID, toolCallID, actionDigest, requested)
}

func (a socketCapabilityAdapter) GrantChat(rootSessionID string, g confine.SocketGrant, grantID, checkpointID, actionDigest string, expiresAt *time.Time) {
	if a.rt == nil {
		return
	}
	a.rt.GrantChat(rootSessionID, g, grantID, checkpointID, actionDigest, expiresAt)
}

func (a socketCapabilityAdapter) IssuePermit(sessionID, toolCallID, actionDigest string, g confine.SocketGrant) {
	if a.rt == nil {
		return
	}
	a.rt.IssuePermit(sessionID, toolCallID, actionDigest, g)
}

func (a socketCapabilityAdapter) ConsumePermit(sessionID, toolCallID, actionDigest string, g confine.SocketGrant) (bool, error) {
	if a.rt == nil {
		return false, nil
	}
	return a.rt.ConsumePermit(sessionID, toolCallID, actionDigest, g)
}

var _ tools.SocketCapabilityRuntime = socketCapabilityAdapter{}
