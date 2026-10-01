package api

// ProgressChangeKind tags one row in a progress_update delta.
type ProgressChangeKind string

const (
	ProgressChangeCreated  ProgressChangeKind = "created"
	ProgressChangeUpdated  ProgressChangeKind = "updated"
	ProgressChangeDone     ProgressChangeKind = "done"
	ProgressChangeNA       ProgressChangeKind = "na"
	ProgressChangeReopened ProgressChangeKind = "reopened"
	ProgressChangeRemoved  ProgressChangeKind = "removed"
)
