// Package workermessaging delivers messages between a coordinator and its
// workers into model requests. Each recipient session gets the oldest
// undelivered page before a request; the page commits only after the
// response succeeds, so a failed request replays it.
package workermessaging

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// Recipient labels for the two directions.
const (
	RecipientCoordinator = "coordinator"
	RecipientWorker      = "worker"
)

// Line and byte bounds across one coordinator or worker request; any remaining
// messages stay queued for later requests.
const (
	maxCoordinatorLines = 32
	maxCoordinatorBytes = workerrecords.MaxHistoryPageBytes
)

// Delivery prepares and commits message pages through the worker records.
type Delivery struct {
	Records *workerrecords.Store
}

var _ assembly.MessageDeliveryRecorder = (*Delivery)(nil)

func actorFor(sess *api.Session) workerrecords.Actor {
	role := workerrecords.RoleCoordinator
	if sess.IsWorkerChild() {
		role = workerrecords.RoleWorker
	}
	return workerrecords.Actor{SessionID: sess.ID, Role: role, PrincipalID: sess.ID}
}

// PrepareMessages opens the session's undelivered pages. A worker gets the
// instructions addressed to it; a coordinator gets fair, byte-bounded slices
// across its active workers ordered by oldest unread message.
func (d *Delivery) PrepareMessages(ctx context.Context, sess *api.Session) (assembly.MessageDelivery, error) {
	if d == nil || d.Records == nil || sess == nil {
		return assembly.MessageDelivery{}, nil
	}
	actor := actorFor(sess)
	generation := int64(sess.CompactionGeneration)
	if sess.IsWorkerChild() {
		w, err := d.Records.Self(ctx, actor)
		if errors.Is(err, workerrecords.ErrWorkerNotFound) {
			return assembly.MessageDelivery{}, nil
		}
		if err != nil {
			return assembly.MessageDelivery{}, err
		}
		out := assembly.MessageDelivery{Recipient: RecipientWorker}
		_, err = d.openPage(ctx, actor, w.ID, w.Name, generation, workerrecords.MaxPageMessages, maxCoordinatorBytes, &out)
		if err != nil {
			return assembly.MessageDelivery{}, err
		}
		return out, nil
	}
	pending, err := d.Records.PendingCoordinatorMessageWorkers(ctx, actor)
	if err != nil {
		return assembly.MessageDelivery{}, err
	}
	out := assembly.MessageDelivery{Recipient: RecipientCoordinator}
	usedBytes := 0
	for i, w := range pending {
		remLines := maxCoordinatorLines - len(out.Lines)
		remBytes := maxCoordinatorBytes - usedBytes
		if remLines <= 0 || remBytes <= 0 {
			out.More = true
			break
		}
		remWorkers := len(pending) - i
		lineQuota := max(1, remLines/remWorkers)
		byteQuota := max(1, remBytes/remWorkers)
		addedBytes, err := d.openPage(ctx, actor, w.ID, w.Name, generation, lineQuota, byteQuota, &out)
		if err != nil {
			return assembly.MessageDelivery{}, err
		}
		usedBytes += addedBytes
	}
	return out, nil
}

func (d *Delivery) openPage(ctx context.Context, actor workerrecords.Actor, workerID, workerName string, generation int64, limit, maxBytes int, out *assembly.MessageDelivery) (int, error) {
	page, err := d.Records.OpenPageBounded(ctx, actor, workerID, generation, limit, maxBytes)
	if err != nil || page.ID == "" {
		return 0, err
	}
	out.Pages = append(out.Pages, assembly.MessagePageRef{WorkerID: workerID, PageID: page.ID, Generation: generation})
	if page.More {
		out.More = true
	}
	addedBytes := 0
	for _, m := range page.Messages {
		addedBytes += len(m.Body) + len(m.ArtifactRef)
		out.Lines = append(out.Lines, inject.WorkerMessageLine{
			Worker: workerName, Seq: m.Seq, ReplyToSeq: m.ReplyToSeq, NeedsReply: m.NeedsReply,
			Body: m.Body, ArtifactRef: m.ArtifactRef,
		})
	}
	return addedBytes, nil
}

// CommitMessages records that the response carrying the pages succeeded.
func (d *Delivery) CommitMessages(ctx context.Context, sessionID string, delivery assembly.MessageDelivery) error {
	if d == nil || d.Records == nil {
		return nil
	}
	role := workerrecords.RoleCoordinator
	if delivery.Recipient == RecipientWorker {
		role = workerrecords.RoleWorker
	}
	actor := workerrecords.Actor{SessionID: sessionID, Role: role, PrincipalID: sessionID}
	for _, page := range delivery.Pages {
		if err := d.Records.CommitPage(ctx, actor, page.WorkerID, page.Generation, page.PageID); err != nil {
			return err
		}
	}
	return nil
}

// RoundFrame reads a worker session's running round for its every request.
func (d *Delivery) RoundFrame(ctx context.Context, sess *api.Session) (inject.WorkerRoundFrame, bool, error) {
	if d == nil || d.Records == nil || sess == nil || !sess.IsWorkerChild() {
		return inject.WorkerRoundFrame{}, false, nil
	}
	frame, ok, err := d.Records.RoundFrame(ctx, actorFor(sess))
	if errors.Is(err, workerrecords.ErrWorkerNotFound) {
		return inject.WorkerRoundFrame{}, false, nil
	}
	if err != nil || !ok {
		return inject.WorkerRoundFrame{}, false, err
	}
	out := inject.WorkerRoundFrame{Ordinal: frame.Ordinal, Brief: frame.Brief, Workspace: frame.Workspace, PriorChange: frame.PriorChange, EarlierResults: frame.EarlierResults}
	for _, m := range frame.Instructions {
		out.Instructions = append(out.Instructions, inject.WorkerMessageLine{Seq: m.Seq, ReplyToSeq: m.ReplyToSeq, Body: m.Body, ArtifactRef: m.ArtifactRef})
	}
	return out, true, nil
}
