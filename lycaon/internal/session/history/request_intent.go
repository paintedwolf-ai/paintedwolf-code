package history

import (
	"context"
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
)

// prepareRequestHistory restores the active instruction before reserving its tokens.
func prepareRequestHistory(ctx context.Context, service *Service, sess *api.Session, history []api.Message, req modelcall.CompletionRequest) ([]api.Message, modelcall.CompletionRequest, error) {
	recall := toolreach.FromContext(ctx).Status("recall") == toolreach.Callable
	var view *store.CompactionView
	if service != nil && (service.sessionStore != nil && service.transcriptStore != nil && service.compactionStore != nil) && sess != nil {
		var err error
		history, err = withCurrentRequest(ctx, service.sessionStore, service.transcriptStore, sess, history)
		if err != nil {
			return nil, req, err
		}
		view = service.requestView(ctx, sess)
	}
	projection := projectRequest(req.Messages, history)
	rows := messageview.PreserveRequestIntent(projection.history, history)
	rows, err := restoreRequestOmissions(ctx, view, history, rows, recall)
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

func restoreRequestOmissions(ctx context.Context, view *store.CompactionView, source, rows []api.Message, recall bool) ([]api.Message, error) {
	if noticeIdx := slices.IndexFunc(rows, func(r api.Message) bool { return r.ID == historyCoverageNoticeID }); noticeIdx >= 0 {
		existing := rows[noticeIdx]
		from, through, ok := resolveExistingOmissionBounds(view, source, rows, noticeIdx)
		var (
			notice *api.Message
			err    error
		)
		if ok {
			notice, err = omissionNoticeForRecall(ctx, recall, historyCoverageNoticeID, from, through)
		} else {
			var covered int64
			if view != nil {
				covered = view.CoveredThroughOrd
			}
			notice, err = omissionFallbackNoticeForRecall(ctx, recall, historyCoverageNoticeID, covered)
		}
		if err != nil {
			return nil, err
		}
		updated := slices.Clone(rows)
		updated[noticeIdx] = preserveAuthorityNoticePrefix(existing, *notice)
		return updated, nil
	}
	if slices.ContainsFunc(rows, isCompactionSummaryOrCheckpoint) {
		return rows, nil
	}
	from, through := omittedOrdinals(source, compaction.ContextMessagesFromAPI(rows))
	if through == 0 {
		return rows, nil
	}
	notice, err := omissionNoticeForRecall(ctx, recall, historyCoverageNoticeID, from, through)
	if err != nil {
		return nil, err
	}
	at := 0
	for at < len(rows) && (rows[at].Ord <= 0 || rows[at].ContextPinned) {
		at++
	}
	return slices.Insert(slices.Clone(rows), at, *notice), nil
}

func resolveExistingOmissionBounds(view *store.CompactionView, source, rows []api.Message, noticeIdx int) (int64, int64, bool) {
	row := rows[noticeIdx]
	if from, through, ok := parseOmissionSignal(row.HostSignalID); ok {
		return from, through, true
	}
	for _, src := range source {
		if src.ID == historyCoverageNoticeID {
			if from, through, ok := parseOmissionSignal(src.HostSignalID); ok {
				return from, through, true
			}
		}
	}
	var covered int64
	if view != nil {
		covered = view.CoveredThroughOrd
	}
	withoutNotice := slices.Delete(slices.Clone(rows), noticeIdx, noticeIdx+1)
	firstDropped, throughDropped := omittedOrdinals(source, compaction.ContextMessagesFromAPI(withoutNotice))
	for _, after := range rows[noticeIdx+1:] {
		if after.Ord > covered && !after.ContextPinned {
			throughDropped = max(throughDropped, after.Ord-1)
			break
		}
	}
	_, from, through := (requestOmission{view: view}).interval(withoutNotice, firstDropped, throughDropped)
	for from <= through && slices.ContainsFunc(withoutNotice, func(r api.Message) bool { return r.Ord == from }) {
		from++
	}
	if from <= 0 || through < from {
		return 0, 0, false
	}
	return from, through, true
}

func preserveAuthorityNoticePrefix(existing, replacement api.Message) api.Message {
	if len(existing.ContentParts) == 0 || existing.ContentParts[0].Source != "content_authority_notice" {
		return replacement
	}
	authPart := existing.ContentParts[0]
	replacement.ContentParts = []api.MessageContentPart{
		authPart,
		{
			Content:   replacement.Content,
			Origin:    replacement.Origin,
			Authority: replacement.Authority,
			TrustTier: replacement.TrustTier,
		},
	}
	replacement.Content = authPart.Content + "\n\n" + replacement.Content
	replacement.ModelProjected = existing.ModelProjected
	return replacement
}
