package assembly

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// WatchDeliveryRecorder reserves undelivered watch matches before a request
// computes resource presence, commits them after a successful response, and
// releases them if rendering or turn execution aborts before commit.
type WatchDeliveryRecorder interface {
	PrepareWatches(ctx context.Context, sess *api.Session, requestKey string) (WatchDelivery, error)
	CommitWatches(ctx context.Context, sessionID string, delivery WatchDelivery) error
	ReleaseWatches(ctx context.Context, sessionID string, delivery WatchDelivery) error
	HasActionableWatches(ctx context.Context, sess *api.Session) (bool, error)
}

// WatchDelivery holds one request's reserved watch match batch.
type WatchDelivery struct {
	DeliveryID string
	RequestKey string
	Block      string
}

func (e *turnContextAssembler) prepareWatchDelivery(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch) error {
	recorder := e.deps.Deliveries.Watches
	if recorder == nil || sess == nil || turn == nil || turn.PromptTurnSeq <= 0 {
		return nil
	}
	if turn.PendingWatches.DeliveryID != "" {
		return nil
	}
	seq := turn.PromptTurnSeq
	reqSeq := turn.WatchRequestSeq
	requestKey := fmt.Sprintf("req:%s:%d:%d", strings.TrimSpace(sess.ID), seq, reqSeq)
	delivery, err := recorder.PrepareWatches(ctx, sess, requestKey)
	if err != nil {
		return err
	}
	turn.PendingWatches = delivery
	return nil
}

func (e *turnContextAssembler) watchDeliveryInject(turn *TurnAssemblyScratch) (api.Message, bool) {
	if turn == nil || strings.TrimSpace(turn.PendingWatches.Block) == "" {
		return api.Message{}, false
	}
	return api.Message{
		Role:      api.MessageRoleSystem,
		Content:   strings.TrimSpace(turn.PendingWatches.Block),
		Origin:    api.MessageOriginHost,
		Authority: api.ContentAuthorityNone,
		TrustTier: api.ContentTrustTierTrusted,
	}, true
}
