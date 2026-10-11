package transcript

import (
	"context"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) GetMessages(ctx context.Context, id string) ([]api.Message, error) {
	msgs, err := m.store.GetMessages(ctx, id)
	if err != nil {
		return nil, err
	}
	return m.Streams.StampMessages(id, msgs), nil
}

// GetTranscriptPage returns one redacted transcript window.
func (m *Service) GetTranscriptPage(ctx context.Context, id string, q api.TranscriptPageQuery) (api.SessionTranscriptPage, error) {
	if m.reconcile != nil {
		_ = m.reconcile.ReconcileOrphanedRuns(ctx, id)
	}
	page, err := m.store.GetTranscriptPage(ctx, id, q)
	if err != nil {
		return page, err
	}
	page.Messages = messageview.TranscriptMessages(m.Streams.StampMessages(id, page.Messages))
	page, err = messageview.BoundTranscriptPage(page, q.After != nil, func(ord int64) (string, error) {
		return store.MessagePages.Encode(pagecursor.Scope(id), store.MessagePosition{Ord: ord})
	})
	if err != nil {
		return page, err
	}
	return m.withPageTurnFacts(ctx, page)
}

// withPageTurnFacts keeps the clocks and receipts of the turns the bounded page
// still opens.
func (m *Service) withPageTurnFacts(ctx context.Context, page api.SessionTranscriptPage) (api.SessionTranscriptPage, error) {
	opened := make(map[string]struct{}, len(page.Messages))
	for _, msg := range page.Messages {
		opened[msg.ID] = struct{}{}
	}
	for openingID := range page.TurnClocks {
		if _, ok := opened[openingID]; !ok {
			delete(page.TurnClocks, openingID)
		}
	}
	loads, err := m.TurnLoadsForPage(ctx, page)
	if err != nil {
		return page, err
	}
	page.TurnLoads = map[string][]api.TurnLoad{}
	for _, load := range loads {
		if load.OpeningMessageID != "" {
			page.TurnLoads[load.OpeningMessageID] = append(page.TurnLoads[load.OpeningMessageID], load)
		}
	}
	return page, nil
}

// TurnLoadsForPage projects the receipts of the turns a transcript page shows.
func (m *Service) TurnLoadsForPage(ctx context.Context, page api.SessionTranscriptPage) ([]api.TurnLoad, error) {
	out := []api.TurnLoad{}
	if m == nil || m.store == nil || len(page.Messages) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(page.Messages))
	for _, msg := range page.Messages {
		if msg.Role == api.MessageRoleUser {
			ids = append(ids, msg.ID)
		}
	}
	receipts, err := m.store.ListTurnLoadReceiptsForTurns(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range receipts {
		out = append(out, TurnLoadWire(r))
	}
	return out, nil
}
