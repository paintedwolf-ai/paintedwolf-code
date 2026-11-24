package watch

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerJobLookup resolves the active worker job ID for a child session when ctx carries none.
type WorkerJobLookup func(ctx context.Context, childSessionID string) string

// Delivery adapts Store to coordinator and worker prompt-assembly watch delivery.
type Delivery struct {
	Store       *Store
	WorkerJobID WorkerJobLookup
}

var _ assembly.WatchDeliveryRecorder = (*Delivery)(nil)

func (d *Delivery) ownerFor(ctx context.Context, sess *api.Session) (Owner, bool) {
	if d == nil || !d.Store.Configured() || sess == nil || strings.TrimSpace(sess.ID) == "" {
		return Owner{}, false
	}
	if sess.IsWorkerChild() {
		jobID := strings.TrimSpace(workercontext.Job(ctx))
		if jobID == "" && d.WorkerJobID != nil {
			jobID = strings.TrimSpace(d.WorkerJobID(ctx, sess.ID))
		}
		if jobID == "" {
			return Owner{}, false
		}
		return Owner{
			Kind:        OwnerJob,
			ProjectID:   strings.TrimSpace(sess.ProjectID),
			SessionID:   strings.TrimSpace(sess.ID),
			WorkerJobID: jobID,
		}, true
	}
	return Owner{
		Kind:      OwnerSession,
		ProjectID: strings.TrimSpace(sess.ProjectID),
		SessionID: strings.TrimSpace(sess.ID),
	}, true
}

func (d *Delivery) PrepareWatches(ctx context.Context, sess *api.Session, requestKey string) (assembly.WatchDelivery, error) {
	owner, ok := d.ownerFor(ctx, sess)
	if !ok {
		return assembly.WatchDelivery{}, nil
	}
	batch, hasMatches, err := ReserveCollectBatch(ctx, d.Store, owner, requestKey)
	if err != nil || !hasMatches {
		return assembly.WatchDelivery{}, err
	}
	return assembly.WatchDelivery{
		DeliveryID: batch.DeliveryID,
		RequestKey: batch.RequestKey,
		Block:      FormatCollectedBatch(batch),
	}, nil
}

func (d *Delivery) CommitWatches(ctx context.Context, sessionID string, delivery assembly.WatchDelivery) error {
	if d == nil || !d.Store.Configured() || delivery.DeliveryID == "" {
		return nil
	}
	return CommitCollectBatch(ctx, d.Store, sessionID, delivery.DeliveryID)
}

func (d *Delivery) ReleaseWatches(ctx context.Context, sessionID string, delivery assembly.WatchDelivery) error {
	if d == nil || !d.Store.Configured() || delivery.DeliveryID == "" {
		return nil
	}
	return ReleaseCollectBatch(ctx, d.Store, sessionID, delivery.DeliveryID)
}

func (d *Delivery) HasActionableWatches(ctx context.Context, sess *api.Session) (bool, error) {
	owner, ok := d.ownerFor(ctx, sess)
	if !ok {
		return false, nil
	}
	return HasActionableWatches(ctx, d.Store, owner)
}
