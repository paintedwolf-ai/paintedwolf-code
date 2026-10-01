package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// QueueSnapshot returns the session's current next-turn draft.
func (m *Manager) QueueSnapshot(id string) api.QueueDraft {
	if m == nil || m.queue == nil {
		return api.QueueDraft{QueueItems: []api.QueueItem{}}
	}
	return m.queue.Snapshot(id)
}

// QueueReorder rearranges the draft to match orderedIDs.
func (m *Manager) QueueReorder(ctx context.Context, id string, expectedRevision uint64, orderedIDs []string) (api.QueueDraft, error) {
	draft, err := m.queue.Reorder(id, expectedRevision, orderedIDs)
	if err == nil {
		m.publishQueue(ctx, id, draft.Revision)
	}
	return draft, err
}

// QueueLink groups the given items to drain as one coalesced turn.
func (m *Manager) QueueLink(ctx context.Context, id string, expectedRevision uint64, ids []string) (api.QueueDraft, error) {
	draft, err := m.queue.Link(id, expectedRevision, ids)
	if err == nil {
		m.publishQueue(ctx, id, draft.Revision)
	}
	return draft, err
}

// QueueUnlink ungroups the given items.
func (m *Manager) QueueUnlink(ctx context.Context, id string, expectedRevision uint64, ids []string) (api.QueueDraft, error) {
	draft, err := m.queue.Unlink(id, expectedRevision, ids)
	if err == nil {
		m.publishQueue(ctx, id, draft.Revision)
	}
	return draft, err
}

// QueueRemove drops items and resolves their admission receipts.
func (m *Manager) QueueRemove(ctx context.Context, id string, expectedRevision uint64, itemIDs []string) (api.QueueDraft, error) {
	draft, err := m.queue.Remove(id, expectedRevision, itemIDs, func() error {
		if m == nil || m.store == nil {
			return nil
		}
		return m.store.CancelQueuedPromptSubmissions(context.WithoutCancel(ctx), itemIDs)
	})
	if err != nil {
		return draft, err
	}
	m.publishQueue(ctx, id, draft.Revision)
	m.publishSessionState(context.WithoutCancel(ctx), id)
	return draft, nil
}

// QueueUpdateText updates the draft and its admission receipt atomically.
func (m *Manager) QueueUpdateText(ctx context.Context, id string, expectedRevision uint64, itemID, text string) (api.QueueDraft, error) {
	draft, err := m.queue.UpdateText(id, expectedRevision, itemID, text, func() error {
		return m.updateQueuedSubmissionText(context.WithoutCancel(ctx), itemID, text)
	})
	if err != nil {
		return draft, err
	}
	m.publishQueue(ctx, id, draft.Revision)
	return draft, nil
}

// updateQueuedSubmissionText re-encodes one queued receipt's input with the edited prose.
func (m *Manager) updateQueuedSubmissionText(ctx context.Context, submissionID, text string) error {
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
	var in PromptInput
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

// QueueSetHold toggles the drain-hold flag.
func (m *Manager) QueueSetHold(ctx context.Context, id string, expectedRevision uint64, hold bool) (api.QueueDraft, error) {
	draft, err := m.queue.SetHold(id, expectedRevision, hold)
	if err == nil {
		m.publishQueue(ctx, id, draft.Revision)
		m.publishSessionState(context.WithoutCancel(ctx), id)
	}
	return draft, err
}

// QueueFireNow moves the given items to the front of the draft (run next).
func (m *Manager) QueueFireNow(ctx context.Context, id string, expectedRevision uint64, ids []string) (api.QueueDraft, error) {
	draft, err := m.queue.MoveFront(id, expectedRevision, ids)
	if err == nil {
		m.publishQueue(ctx, id, draft.Revision)
	}
	return draft, err
}

// QueueSend reserves the head group for the active turn's next boundary.
func (m *Manager) QueueSend(ctx context.Context, id string, expectedRevision uint64) (api.QueueDraft, error) {
	sess, err := m.store.Get(ctx, id)
	if err != nil {
		return m.QueueSnapshot(id), err
	}
	draft, err := m.queue.RequestSend(id, expectedRevision, func(items []api.QueueItem) error {
		if sess.Status != api.SessionStatusBusy {
			return nil
		}
		return m.markQueuedSubmissionsContinuation(context.WithoutCancel(ctx), items)
	})
	if err != nil {
		return draft, err
	}
	m.publishQueue(ctx, id, draft.Revision)
	m.publishSessionState(context.WithoutCancel(ctx), id)
	return draft, nil
}

// QueueCancelSend returns an unclaimed reservation to the editable queue.
func (m *Manager) QueueCancelSend(ctx context.Context, id string, expectedRevision uint64) (api.QueueDraft, error) {
	draft, err := m.queue.CancelSend(id, expectedRevision, func(released []api.QueueItem) error {
		return m.clearQueuedSubmissionsContinuation(context.WithoutCancel(ctx), released)
	})
	if err != nil {
		return draft, err
	}
	m.publishQueue(ctx, id, draft.Revision)
	m.publishSessionState(context.WithoutCancel(ctx), id)
	return draft, nil
}

func (m *Manager) clearQueuedSubmissionsContinuation(ctx context.Context, items []api.QueueItem) error {
	return m.setQueuedSubmissionsContinuation(ctx, items, false)
}

func (m *Manager) markQueuedSubmissionsContinuation(ctx context.Context, items []api.QueueItem) error {
	return m.setQueuedSubmissionsContinuation(ctx, items, true)
}

func (m *Manager) setQueuedSubmissionsContinuation(ctx context.Context, items []api.QueueItem, continuation bool) error {
	updates := make([]store.PromptSubmissionInputUpdate, 0, len(items))
	for _, item := range items {
		row, err := m.store.GetPromptSubmission(ctx, item.ID)
		if err != nil {
			return fmt.Errorf("read queued send receipt %s: %w", item.ID, err)
		}
		in, err := decodeQueuedSubmissionInput(row)
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

// DrainQueue bypasses round-idle gating for a ready draft.
func (m *Manager) DrainQueue(ctx context.Context, id string) {
	if m == nil || m.store == nil {
		return
	}
	ctx, unlockDispatch := m.lockPromptSubmissionDispatch(ctx, id)
	defer unlockDispatch()
	if _, err := m.dispatchPromptSubmissions(ctx, id, ""); err != nil {
		m.logTurnFailure(ctx, id, err)
	}
}
