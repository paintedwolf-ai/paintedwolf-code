package findings

import "github.com/lycaon/lycaon/internal/revision"

var revisions = revision.NewCounter()

// CurrentRevision returns the current findings revision for sessionID.
func CurrentRevision(sessionID string) uint64 {
	return revisions.Get(sessionID)
}

// BumpRevision increments and returns the findings revision for sessionID.
func BumpRevision(sessionID string) uint64 {
	return revisions.Bump(sessionID)
}
