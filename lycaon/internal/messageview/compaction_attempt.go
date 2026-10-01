package messageview

import "context"

// CompactionAttempt remembers the last unproductive summary for an exact input.
// One row per session bounds storage; transient provider failures are not cached.
type CompactionAttempt struct {
	Revision string
	Reason   string
}

type CompactionAttemptStore interface {
	GetCompactionAttempt(context.Context, string) (CompactionAttempt, bool, error)
	PutCompactionAttempt(context.Context, string, CompactionAttempt) error
}
