package store

import (
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/pkg/api"
)

// MessagePosition is the sealed position inside a session transcript.
type MessagePosition struct {
	Ord int64 `json:"ord"`
}

// MessagePages encodes and decodes message window pagination cursors.
var MessagePages = pagecursor.For[MessagePosition]("session_messages")

// SetTranscriptPageCursors populates BeforeCursor and AfterCursor on page.
func SetTranscriptPageCursors(sessionID string, page *api.SessionTranscriptPage, hasBefore, hasAfter bool, q api.TranscriptPageQuery) error {
	if hasBefore {
		var oldestOrd int64
		if len(page.Messages) > 0 {
			oldestOrd = page.Messages[0].Ord
		} else if q.Before != nil {
			oldestOrd = *q.Before
		}
		c, err := MessagePages.Encode(pagecursor.Scope(sessionID), MessagePosition{Ord: oldestOrd})
		if err != nil {
			return err
		}
		page.BeforeCursor = c
	}
	if hasAfter {
		var newestOrd int64
		if len(page.Messages) > 0 {
			newestOrd = page.Messages[len(page.Messages)-1].Ord
		} else if q.After != nil {
			newestOrd = *q.After
		}
		c, err := MessagePages.Encode(pagecursor.Scope(sessionID), MessagePosition{Ord: newestOrd})
		if err != nil {
			return err
		}
		page.AfterCursor = c
	}
	return nil
}
