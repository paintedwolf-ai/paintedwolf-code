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

// maxCoordinatorLines bounds how many messages one coordinator request
// carries across its workers; the rest wait for the next request.
const maxCoordinatorLines = 32

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
// instructions addressed to it; a coordinator gets what its workers said.
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
		if err := d.openPage(ctx, actor, w, generation, &out); err != nil {
			return assembly.MessageDelivery{}, err
		}
		return out, nil
	}
	workers, err := d.Records.Workers(ctx, actor)
	if err != nil {
		return assembly.MessageDelivery{}, err
	}
	out := assembly.MessageDelivery{Recipient: RecipientCoordinator}
	for _, w := range workers {
		if len(out.Lines) >= maxCoordinatorLines {
			out.More = true
			break
		}
		if err := d.openPage(ctx, actor, w, generation, &out); err != nil {
			return assembly.MessageDelivery{}, err
		}
	}
	return out, nil
}

func (d *Delivery) openPage(ctx context.Context, actor workerrecords.Actor, w workerrecords.Worker, generation int64, out *assembly.MessageDelivery) error {
	page, err := d.Records.OpenPage(ctx, actor, w.ID, generation)
	if err != nil || page.ID == "" {
		return err
	}
	out.Pages = append(out.Pages, assembly.MessagePageRef{WorkerID: w.ID, PageID: page.ID, Generation: generation})
	if len(page.Messages) == workerrecords.MaxPageMessages {
		out.More = true
	}
	for _, m := range page.Messages {
		out.Lines = append(out.Lines, inject.WorkerMessageLine{
			Worker: w.Name, Seq: m.Seq, ReplyToSeq: m.ReplyToSeq, NeedsReply: m.NeedsReply,
			Body: m.Body, ArtifactRef: m.ArtifactRef,
		})
	}
	return nil
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
