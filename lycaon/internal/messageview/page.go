package messageview

import (
	"encoding/json"

	"github.com/lycaon/lycaon/pkg/api"
)

const TranscriptPageBytes = 256 * 1024

// BoundTranscriptPage preserves cursor direction and always admits one atomic row.
func BoundTranscriptPage(page api.SessionTranscriptPage, forward bool, encodeCursor func(ord int64) (string, error)) (api.SessionTranscriptPage, error) {
	bytes, kept := 0, 0
	for kept < len(page.Messages) {
		index := kept
		if !forward {
			index = len(page.Messages) - 1 - kept
		}
		body, err := json.Marshal(page.Messages[index])
		if err != nil {
			return page, err
		}
		if kept > 0 && bytes+len(body) > TranscriptPageBytes {
			break
		}
		bytes += len(body)
		kept++
	}
	if kept == len(page.Messages) {
		return page, nil
	}
	var err error
	if forward {
		page.Messages = page.Messages[:kept]
		page.AfterCursor, err = encodeCursor(page.Messages[kept-1].Ord)
	} else {
		page.Messages = page.Messages[len(page.Messages)-kept:]
		page.BeforeCursor, err = encodeCursor(page.Messages[0].Ord)
	}
	return page, err
}
