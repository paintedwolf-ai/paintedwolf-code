package store

import (
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// CompactionView stores compacted history and its immutable ordinal watermark.
// SourceSeq detects changes to covered rows.
type CompactionView struct {
	Generation        int
	Messages          []api.Message
	CoveredThroughOrd int64
	CoveredThroughID  string
	SourceSeq         int64
	TokensBefore      int
	TokensAfter       int
	CreatedAt         time.Time
}
