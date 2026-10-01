package tools

import (
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
)

// DirectIPCapabilityRuntime holds one-action permits and chat leases.
type DirectIPCapabilityRuntime interface {
	IssuePermit(sessionID, toolCallID, actionDigest, requestDigest, confinementDigest string)
	ConsumePermit(sessionID, toolCallID, actionDigest, requestDigest, confinementDigest string) (bool, error)
	Authorized(sessionID, toolCallID, actionDigest string) bool
	// LeaseCovers matches live exact-action authority.
	LeaseCovers(chatSessionID string, lease hitl.DirectIPLease) bool
	// GrantChat records an accepted exact-action lease.
	GrantChat(chatSessionID string, lease hitl.DirectIPLease, grantID, checkpointID string, expiresAt *time.Time)
}
