package inject

import (
	"context"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	executionStatePageRows = 32
	// A window this long without enough aging batches is mostly polling. Rows
	// beyond it leave early rather than extend the read.
	executionStateMaxRows = 256
)

// MessageWindowReader reads canonical transcript rows, which compaction and
// restart leave intact.
type MessageWindowReader func(ctx context.Context, sessionID string, window messageview.MessageWindow) ([]api.Message, error)

// ReadExecutionStateWindow walks back from the newest row until the current
// intent opens or the batches that can still hold a live reminder are complete.
func ReadExecutionStateWindow(ctx context.Context, read MessageWindowReader, sessionID string) ([]api.Message, error) {
	var window []api.Message
	var beforeOrd int64
	for len(window) < executionStateMaxRows {
		limit := min(executionStatePageRows, executionStateMaxRows-len(window))
		page, err := read(ctx, sessionID, messageview.MessageWindow{BeforeOrd: beforeOrd, Limit: limit})
		if err != nil {
			return nil, err
		}
		window = append(append(make([]api.Message, 0, len(page)+len(window)), page...), window...)
		if len(page) < limit || api.UserIntentBoundary(page) > 0 || page[0].Ord <= 1 || agingBatchCount(window) > MaxRejectedOperationStepAge {
			break
		}
		beforeOrd = page[0].Ord
	}
	return window, nil
}

func agingBatchCount(messages []api.Message) int {
	_, aging := agingExecutionBatches(messages)
	count := 0
	for _, ages := range aging {
		if ages {
			count++
		}
	}
	return count
}
