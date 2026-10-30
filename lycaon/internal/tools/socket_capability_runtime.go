package tools

import (
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

// SocketCapabilityRuntime holds session-tree AF_UNIX chat grants and ephemeral permits.
type SocketCapabilityRuntime interface {
	AppliedGrants(rootSessionID string) []confine.SocketGrant
	AuthorizedGrants(rootSessionID string, identity hitl.EffectPermitIdentity, actionDigest string, requested []confine.SocketGrant) []confine.SocketGrant
	GrantChat(rootSessionID string, g confine.SocketGrant, grantID, checkpointID, actionDigest string, expiresAt *time.Time) bool
	IssuePermit(identity hitl.EffectPermitIdentity, actionDigest string, g confine.SocketGrant)
	ConsumePermit(identity hitl.EffectPermitIdentity, actionDigest string, g confine.SocketGrant) (bool, error)
}

// DurableSocketSource supplies project/device exact AF_UNIX grants for the next
// boundary, keyed by project identity. Folders change under a project; the
// leases the person granted it do not.
type DurableSocketSource func(projectID string) []confine.SocketGrant
