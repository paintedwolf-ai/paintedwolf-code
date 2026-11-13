package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/pkg/api"
)

// MessageDeliveryRecorder prepares the undelivered message page for a
// session's next request and commits it after a successful response.
type MessageDeliveryRecorder interface {
	PrepareMessages(ctx context.Context, sess *api.Session) (MessageDelivery, error)
	CommitMessages(ctx context.Context, sessionID string, delivery MessageDelivery) error
}

// MessageDelivery is one request's message page. Pages name the outstanding
// delivery page per worker, so a failed request replays them.
type MessageDelivery struct {
	Recipient string
	Lines     []inject.WorkerMessageLine
	More      bool
	Pages     []MessagePageRef
}

// MessagePageRef names one worker's outstanding page.
type MessagePageRef struct {
	WorkerID   string
	PageID     string
	Generation int64
}

// workerMessagesInject renders the page the recorder prepared, for a
// coordinator or a worker alike.
func (e *turnContextAssembler) workerMessagesInject(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch) (api.Message, bool, error) {
	recorder := e.deps.Deliveries.Messages
	if recorder == nil || sess == nil {
		return api.Message{}, false, nil
	}
	delivery, err := recorder.PrepareMessages(ctx, sess)
	if err != nil {
		return api.Message{}, false, err
	}
	if turn != nil {
		turn.PendingMessages = delivery
	}
	block := inject.RenderWorkerMessagesBlock(ctx, e.deps.Injects, sess.ID, delivery.Recipient, delivery.Lines, delivery.More)
	if block.Header == "" {
		return api.Message{}, false, nil
	}
	return api.Message{
		Role: api.MessageRoleSystem, Content: strings.TrimSpace(block.Header + "\n\n" + block.Messages),
		Origin: api.MessageOriginHost, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted,
		ContentParts: inject.WorkerMessagesParts(block, delivery.Recipient),
	}, true, nil
}
