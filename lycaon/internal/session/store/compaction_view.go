package store

import (
	"errors"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/messageview"

	"github.com/lycaon/lycaon/pkg/api"
)

var errCompactionSourceChanged = errors.New("compaction source changed")

func validateMemoryCompactionSource(messages []api.Message, clock int64, view CompactionView) error {
	if clock < view.SourceSeq || clock-view.SourceSeq > messageview.MaxCompactionRevisionLag {
		return errCompactionSourceChanged
	}
	boundary := view.CoveredThroughOrd == 0 && strings.TrimSpace(view.CoveredThroughID) == ""
	for _, message := range messages {
		if message.Ord <= view.CoveredThroughOrd && message.Seq > view.SourceSeq {
			return errCompactionSourceChanged
		}
		boundary = boundary || message.Ord == view.CoveredThroughOrd && message.ID == view.CoveredThroughID
	}
	if !boundary {
		return errCompactionSourceChanged
	}
	return nil
}

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
