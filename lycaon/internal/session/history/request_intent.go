package history

import (
	"context"
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// prepareRequestHistory restores the active instruction before reserving its tokens.
func prepareRequestHistory(ctx context.Context, store Store, sess *api.Session, history []api.Message, req modelcall.CompletionRequest, recall func(context.Context, *api.Session) bool) ([]api.Message, modelcall.CompletionRequest, error) {
	if store != nil && sess != nil {
		var err error
		history, err = withCurrentRequest(ctx, store, sess, history)
		if err != nil {
			return nil, req, err
		}
	}
	projection := projectRequest(req.Messages, history)
	rows := retainRequestIntent(projection.history, history)
	rows, err := restoreRequestOmissions(ctx, sess, history, rows, recall)
	if err != nil {
		return nil, req, err
	}
	req.Messages = projection.replace(rows)
	return history, req, nil
}

// retainRequestIntent preserves the original instruction through view replacement.
func retainRequestIntent(rows, source []api.Message) []api.Message {
	intent, ok := api.LastUserIntentMessage(source)
	if !ok {
		return rows
	}
	intent.ContextPinned = true
	for i, row := range rows {
		if row.ID == intent.ID {
			out := slices.Clone(rows)
			out[i].ContextPinned = true
			return out
		}
	}
	at := 0
	for at < len(rows) && rows[at].Ord <= 0 {
		at++
	}
	return slices.Insert(slices.Clone(rows), at, intent)
}

func validateRequestIntent(source, messages []api.Message) error {
	intent, ok := api.LastUserIntentMessage(source)
	if !ok {
		return nil
	}
	for _, row := range messages {
		if row.ID == intent.ID && api.IsUserIntentMessage(row) {
			return nil
		}
	}
	return fmt.Errorf("fitted request lost active instruction %q", intent.ID)
}

func restoreRequestOmissions(ctx context.Context, sess *api.Session, source, rows []api.Message, recall func(context.Context, *api.Session) bool) ([]api.Message, error) {
	for _, row := range rows {
		if isCompactionSummaryOrCheckpoint(row) || row.ID == historyCoverageNoticeID {
			return rows, nil
		}
	}
	from, through := omittedOrdinals(source, compaction.ContextMessagesFromAPI(rows))
	if through == 0 {
		return rows, nil
	}
	notice, err := omissionNotice(ctx, recall, sess, historyCoverageNoticeID, from, through)
	if err != nil {
		return nil, err
	}
	at := 0
	for at < len(rows) && (rows[at].Ord <= 0 || rows[at].ContextPinned) {
		at++
	}
	return slices.Insert(slices.Clone(rows), at, *notice), nil
}
