package progress

import "github.com/lycaon/lycaon/internal/revision"

var revisions = revision.NewCounter()

// CurrentRevision returns the current progress revision for sessionID.
func CurrentRevision(sessionID string) uint64 {
	return revisions.Get(sessionID)
}

// BumpRevision increments and returns the progress revision for sessionID.
func BumpRevision(sessionID string) uint64 {
	return revisions.Bump(sessionID)
}
