package draftqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	Get(context.Context, string) (*api.Session, error)
	CancelQueuedPromptSubmissions(context.Context, []string) error
	GetPromptSubmission(context.Context, string) (*store.PromptSubmission, error)
	UpdateQueuedPromptSubmissionInputs(context.Context, []store.PromptSubmissionInputUpdate) (bool, error)
}
type Service struct {
	store        Store
	queue        *queue.Store
	publisher    *events.Publisher
	publishState func(context.Context, string)
}

func New(store Store, queue *queue.Store, publishState func(context.Context, string)) *Service {
	return &Service{store: store, queue: queue, publishState: publishState}
}
func (s *Service) SetPublisher(publisher *events.Publisher) { s.publisher = publisher }
func (s *Service) Publish(ctx context.Context, sessionID string, revision uint64) {
	if s.publisher != nil {
		s.publisher.PublishQueue(ctx, sessionID, revision)
	}
}

// Snapshot returns the session's current next-turn draft.
func (m *Service) Snapshot(id string) api.QueueDraft {
	if m == nil || m.queue == nil {
		return api.QueueDraft{QueueItems: []api.QueueItem{}}
	}
	return m.queue.Snapshot(id)
}

// Reorder rearranges the draft to match orderedIDs.
func (m *Service) Reorder(ctx context.Context, id string, expectedRevision uint64, orderedIDs []string) (api.QueueDraft, error) {
	draft, err := m.queue.Reorder(id, expectedRevision, orderedIDs)
	if err == nil {
		m.Publish(ctx, id, draft.Revision)
	}
	return draft, err
}

// Link groups the given items to drain as one coalesced turn.
func (m *Service) Link(ctx context.Context, id string, expectedRevision uint64, ids []string) (api.QueueDraft, error) {
	draft, err := m.queue.Link(id, expectedRevision, ids)
	if err == nil {
		m.Publish(ctx, id, draft.Revision)
	}
	return draft, err
}

// Unlink ungroups the given items.
func (m *Service) Unlink(ctx context.Context, id string, expectedRevision uint64, ids []string) (api.QueueDraft, error) {
	draft, err := m.queue.Unlink(id, expectedRevision, ids)
	if err == nil {
		m.Publish(ctx, id, draft.Revision)
	}
	return draft, err
}

// Remove drops items and resolves their admission receipts.
func (m *Service) Remove(ctx context.Context, id string, expectedRevision uint64, itemIDs []string) (api.QueueDraft, error) {
	draft, err := m.queue.Remove(id, expectedRevision, itemIDs, func() error {
		if m == nil || m.store == nil {
			return nil
		}
		return m.store.CancelQueuedPromptSubmissions(context.WithoutCancel(ctx), itemIDs)
	})
	if err != nil {
		return draft, err
	}
	m.Publish(ctx, id, draft.Revision)
	m.publishState(context.WithoutCancel(ctx), id)
	return draft, nil
}

// UpdateText updates the draft and its admission receipt atomically.
func (m *Service) UpdateText(ctx context.Context, id string, expectedRevision uint64, itemID, text string) (api.QueueDraft, error) {
	draft, err := m.queue.UpdateText(id, expectedRevision, itemID, text, func() error {
		return m.updateQueuedSubmissionText(context.WithoutCancel(ctx), itemID, text)
	})
	if err != nil {
		return draft, err
	}
	m.Publish(ctx, id, draft.Revision)
	return draft, nil
}

// updateQueuedSubmissionText re-encodes one queued receipt's input with the edited prose.
func (m *Service) updateQueuedSubmissionText(ctx context.Context, submissionID, text string) error {
	if m == nil || m.store == nil {
		return nil
	}
	row, err := m.store.GetPromptSubmission(ctx, submissionID)
	if err != nil {
		if errors.Is(err, store.ErrPromptSubmissionNotFound) {
			return nil
		}
		return fmt.Errorf("read queued prompt receipt for inline edit: %w", err)
	}
	var in promptinput.Input
	if err := json.Unmarshal([]byte(row.InputJSON), &in); err != nil {
		return fmt.Errorf("decode queued prompt receipt input: %w", err)
	}
	in.Text = text
	raw, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode queued prompt receipt input: %w", err)
	}
	updated, err := m.store.UpdateQueuedPromptSubmissionInputs(ctx, []store.PromptSubmissionInputUpdate{{
		ID: submissionID, InputJSON: string(raw),
	}})
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("queued prompt submission %s is no longer editable", submissionID)
	}
	return nil
}

// SetHold toggles the drain-hold flag.
func (m *Service) SetHold(ctx context.Context, id string, expectedRevision uint64, hold bool) (api.QueueDraft, error) {
	draft, err := m.queue.SetHold(id, expectedRevision, hold)
	if err == nil {
		m.Publish(ctx, id, draft.Revision)
		m.publishState(context.WithoutCancel(ctx), id)
	}
	return draft, err
}

// FireNow moves the given items to the front of the draft (run next).
func (m *Service) FireNow(ctx context.Context, id string, expectedRevision uint64, ids []string) (api.QueueDraft, error) {
	draft, err := m.queue.MoveFront(id, expectedRevision, ids)
	if err == nil {
		m.Publish(ctx, id, draft.Revision)
	}
	return draft, err
}

// Send reserves the head group for the active turn's next boundary.
func (m *Service) Send(ctx context.Context, id string, expectedRevision uint64) (api.QueueDraft, error) {
	sess, err := m.store.Get(ctx, id)
	if err != nil {
		return m.Snapshot(id), err
	}
	draft, err := m.queue.RequestSend(id, expectedRevision, func(items []api.QueueItem) error {
		if sess.Status != api.SessionStatusBusy {
			return nil
		}
		return m.setContinuation(context.WithoutCancel(ctx), items, true)
	})
	if err != nil {
		return draft, err
	}
	m.Publish(ctx, id, draft.Revision)
	m.publishState(context.WithoutCancel(ctx), id)
	return draft, nil
}

// CancelSend returns an unclaimed reservation to the editable queue.
func (m *Service) CancelSend(ctx context.Context, id string, expectedRevision uint64) (api.QueueDraft, error) {
	draft, err := m.queue.CancelSend(id, expectedRevision, func(released []api.QueueItem) error {
		return m.setContinuation(context.WithoutCancel(ctx), released, false)
	})
	if err != nil {
		return draft, err
	}
	m.Publish(ctx, id, draft.Revision)
	m.publishState(context.WithoutCancel(ctx), id)
	return draft, nil
}

func (m *Service) setContinuation(ctx context.Context, items []api.QueueItem, continuation bool) error {
	updates := make([]store.PromptSubmissionInputUpdate, 0, len(items))
	for _, item := range items {
		row, err := m.store.GetPromptSubmission(ctx, item.ID)
		if err != nil {
			return fmt.Errorf("read queued send receipt %s: %w", item.ID, err)
		}
		in, err := promptinput.FromSubmission(row)
		if err != nil {
			return fmt.Errorf("decode queued send receipt %s: %w", item.ID, err)
		}
		in.Continuation = continuation
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode queued send receipt %s: %w", item.ID, err)
		}
		updates = append(updates, store.PromptSubmissionInputUpdate{ID: item.ID, InputJSON: string(raw)})
	}
	updated, err := m.store.UpdateQueuedPromptSubmissionInputs(ctx, updates)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("queued send receipts are no longer editable")
	}
	return nil
}
