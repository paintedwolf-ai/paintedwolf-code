package history

import (
	"context"
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

// prepareRequestHistory restores the active instruction before reserving its tokens.
func prepareRequestHistory(ctx context.Context, service *Service, sess *api.Session, history []api.Message, req modelcall.CompletionRequest) ([]api.Message, modelcall.CompletionRequest, error) {
	var recall func(context.Context, *api.Session) bool
	if service != nil {
		recall = service.recallAvailable
	}
	if service != nil && service.store != nil && sess != nil {
		var err error
		history, err = withCurrentRequest(ctx, service.store, sess, history)
		if err != nil {
			return nil, req, err
		}
	}
	projection := projectRequest(req.Messages, history)
	rows := messageview.PreserveRequestIntent(projection.history, history)
	rows, err := restoreRequestOmissions(ctx, sess, history, rows, recall)
	if err != nil {
		return nil, req, err
	}
	req.Messages = projection.replace(rows)
	return history, req, nil
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
