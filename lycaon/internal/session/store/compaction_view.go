package store

import (
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/messageview"

	"github.com/lycaon/lycaon/pkg/api"
)

var errCompactionSourceChanged = errors.New("compaction source changed")

// CompactionView stores compacted history and its immutable ordinal watermark.
// SourceSeq detects changes to covered rows.
type CompactionView struct {
	Continuation      *messageview.Continuation
	Generation        int
	Messages          []api.Message
	CoveredThroughOrd int64
	CoveredThroughID  string
	SourceSeq         int64
	TokensBefore      int
	TokensAfter       int
	CreatedAt         time.Time
}
