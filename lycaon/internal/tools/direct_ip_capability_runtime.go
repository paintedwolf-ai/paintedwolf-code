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

// DirectIPLifecyclePhase is a typed lifecycle moment for one-action direct IP.
type DirectIPLifecyclePhase string

const (
	DirectIPLifecycleRequested     DirectIPLifecyclePhase = "requested"
	DirectIPLifecycleApproved      DirectIPLifecyclePhase = "approved"
	DirectIPLifecycleLeaseReused   DirectIPLifecyclePhase = "lease_reused"
	DirectIPLifecycleDenied        DirectIPLifecyclePhase = "denied"
	DirectIPLifecycleStarted       DirectIPLifecyclePhase = "started"
	DirectIPLifecycleCompleted     DirectIPLifecyclePhase = "completed"
	DirectIPLifecycleReconstructed DirectIPLifecyclePhase = "reconstructed"
)

// DirectIPLifecycleEvent is consumed by capability audit surfaces.
type DirectIPLifecycleEvent struct {
	Phase                DirectIPLifecyclePhase
	SessionID            string
	ToolCallID           string
	ActionDigest         string
	AuthorizationSource  string
	Visibility           string // fixed unobserved
	DeclaredDestinations []string
	Background           bool
}

// DirectIPLifecycleHook receives typed unobserved direct lifecycle facts.
type DirectIPLifecycleHook func(DirectIPLifecycleEvent)
