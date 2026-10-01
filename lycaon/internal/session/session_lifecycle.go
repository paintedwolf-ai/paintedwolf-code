package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// ErrSessionBusy rejects a hard delete while a turn is running.
var ErrSessionBusy = errors.New("session has a turn in flight")

// ErrWorkerChildLifecycle rejects chat lifecycle changes on worker sessions.
var ErrWorkerChildLifecycle = errors.New("worker child sessions have no chat lifecycle")

// ErrSessionWorktreeBound protects a checkout bound to a session.
var ErrSessionWorktreeBound = errors.New("unbind this chat's worktree before deleting it")

// ErrInvalidSessionListCursor rejects a page cursor that does not belong to
// the query. It wraps the pagecursor.ErrInvalid or pagecursor.ErrExpired failure.
var ErrInvalidSessionListCursor = errors.New("invalid session list cursor")

var sessionListPages = pagecursor.For[store.SummaryCursor]("session_list")

// ListProjectSessions pages a project's chat sessions.
func (m *Manager) ListProjectSessions(ctx context.Context, q store.SummaryQuery) (wire.SessionListPage, error) {
	if m == nil || m.store == nil {
		return wire.SessionListPage{}, fmt.Errorf("session manager not configured")
	}
	if q.Cursor != "" {
		cursor, err := sessionListPages.Decode(q.Cursor, sessionListScope(q))
		if err != nil {
			return wire.SessionListPage{}, fmt.Errorf("%w: %w", ErrInvalidSessionListCursor, err)
		}
		q.After = &cursor
	}
	result, err := m.store.ListProjectSummaries(ctx, q)
	if err != nil {
		return wire.SessionListPage{}, err
	}
	page := wire.SessionListPage{
		Sessions: result.Sessions,
		Total:    result.Total,
	}
	if result.Next != nil {
		page.NextCursor, err = sessionListPages.Encode(sessionListScope(q), *result.Next)
		if err != nil {
			return wire.SessionListPage{}, err
		}
	}
	return page, nil
}

func sessionListScope(q store.SummaryQuery) string {
	pinned := "any"
	if q.Pinned != nil {
		pinned = fmt.Sprintf("%t", *q.Pinned)
	}
	return pagecursor.Scope(strings.TrimSpace(q.ProjectID), fmt.Sprintf("%t", q.Archived), pinned,
		strings.ToLower(strings.TrimSpace(q.TitleQuery)), string(q.Sort), string(q.Order))
}

// SetArchived changes session list membership.
func (m *Manager) SetArchived(ctx context.Context, id string, archived bool) (*wire.Session, error) {
	return m.changeLifecycle(ctx, id, func(id string) error {
		return m.store.UpdateSession(ctx, id, func(sess *wire.Session) {
			if !archived {
				sess.ArchivedAt = nil
			} else if sess.ArchivedAt == nil {
				now := time.Now().UTC()
				sess.ArchivedAt = &now
			}
		})
	})
}

// SetPinned adds a chat to the end of its project's pinned order, or removes it.
func (m *Manager) SetPinned(ctx context.Context, id string, pinned bool) (*wire.Session, error) {
	return m.changeLifecycle(ctx, id, func(id string) error {
		if pinned {
			return m.store.PinSession(ctx, id)
		}
		return m.store.UnpinSession(ctx, id)
	})
}

// MovePinned places a pinned chat at a 1-based position in its project's pinned order.
func (m *Manager) MovePinned(ctx context.Context, id string, position int) (*wire.Session, error) {
	return m.changeLifecycle(ctx, id, func(id string) error {
		return m.store.MovePinnedSession(ctx, id, position)
	})
}

// changeLifecycle applies one chat lifecycle write and publishes the result.
func (m *Manager) changeLifecycle(ctx context.Context, id string, write func(id string) error) (*wire.Session, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("session manager not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, store.ErrSessionNotFound
	}
	current, err := m.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.IsWorkerChild() {
		return nil, ErrWorkerChildLifecycle
	}
	if err := write(id); err != nil {
		return nil, err
	}
	sess, err := m.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	m.publishSessionLifecycle(ctx, sess)
	return sess, nil
}

// DeleteSession disposes live children before removing session history.
func (m *Manager) DeleteSession(ctx context.Context, id string) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("session manager not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return store.ErrSessionNotFound
	}
	lock := m.promptState.Prompt.Acquire(id)
	if !lock.TryLock() {
		return ErrSessionBusy
	}
	defer lock.Unlock()
	sess, err := m.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if sess.IsWorkerChild() {
		return ErrWorkerChildLifecycle
	}
	if sess.Status == wire.SessionStatusBusy {
		return ErrSessionBusy
	}
	if _, bound, err := m.store.GetWorktreeBinding(ctx, id); err != nil {
		return err
	} else if bound {
		return ErrSessionWorktreeBound
	}
	if m.sessionWorkerAbort != nil {
		if err := m.sessionWorkerAbort.AbortAllWorkers(ctx, id, sess.ProjectID, "session deleted"); err != nil {
			return fmt.Errorf("dispose session workers: %w", err)
		}
	}
	if err := m.disposeSessionRuntime(ctx, id); err != nil {
		return fmt.Errorf("dispose session runtime: %w", err)
	}
	checkpointStore, checkpointErr := m.sessionCheckpointStore(ctx, id)
	if errors.Is(checkpointErr, errCheckpointRootUnset) {
		checkpointErr = nil
	}
	tree, err := m.store.SessionTreeIDs(ctx, id)
	if err != nil {
		return err
	}
	if err := m.store.Delete(ctx, id); err != nil {
		return err
	}
	if m.resources != nil {
		if err := m.resources.ForgetDisposed(resourcelifecycle.SessionScope(id)); err != nil {
			slog.WarnContext(ctx, "forget deleted session resource scope", "session_id", id, "error", err)
		}
	}
	if checkpointErr == nil && checkpointStore != nil {
		checkpointErr = m.checkpointCapture.DropSession(ctx, checkpointStore, id)
	}
	if checkpointErr != nil {
		slog.WarnContext(ctx, "remove deleted session checkpoints", "session_id", id, "error", checkpointErr)
	}
	m.removeScratch(ctx, tree)
	if m.loopbackProv != nil {
		m.loopbackProv.ForgetSession(id)
	}
	m.stopState.Forget(id)
	m.publishSessionDeleted(ctx, sess)
	if m.events != nil {
		m.events.PublishAttention(ctx)
	}
	return nil
}

func (m *Manager) disposeSessionRuntime(ctx context.Context, id string) error {
	if m == nil {
		return nil
	}
	if m.resources != nil {
		if err := m.resources.Dispose(ctx, resourcelifecycle.SessionScope(id)); err != nil {
			return err
		}
	}
	if m.queue != nil {
		m.queue.Clear(id)
	}
	return nil
}

func (m *Manager) releaseSessionRuntime(ctx context.Context, id string) error {
	if m == nil || m.resources == nil {
		return nil
	}
	return m.resources.Release(ctx, resourcelifecycle.SessionScope(id))
}

func (m *Manager) publishSessionDeleted(ctx context.Context, sess *wire.Session) {
	if m == nil || m.events == nil || m.events.Hub == nil || sess == nil {
		return
	}
	if m.mutationEventsOutboxed() {
		return
	}
	key := events.PublishKey{
		Project: strings.TrimSpace(sess.ProjectID),
		Session: strings.TrimSpace(sess.ID),
	}
	_ = m.events.Hub.Publish(ctx, wire.EventTopicSession, key, wire.SessionEvent{
		ID:        sess.ID,
		ProjectID: sess.ProjectID,
		Action:    wire.SessionEventActionDeleted,
		Status:    sess.Status,
	})
}

// maybeUnarchiveOnPrompt restores a prompted session to the working set.
func (m *Manager) maybeUnarchiveOnPrompt(ctx context.Context, sess *wire.Session) error {
	if m == nil || m.store == nil || sess == nil || sess.ArchivedAt == nil {
		return nil
	}
	if err := m.store.UpdateSession(ctx, sess.ID, func(s *wire.Session) {
		s.ArchivedAt = nil
	}); err != nil {
		return err
	}
	sess.ArchivedAt = nil
	if fresh, err := m.store.Get(ctx, sess.ID); err == nil {
		m.publishSessionLifecycle(ctx, fresh)
	}
	return nil
}

// publishSessionLifecycle refreshes session lists and attention state.
func (m *Manager) publishSessionLifecycle(ctx context.Context, sess *wire.Session) {
	if m == nil || m.events == nil || m.events.Hub == nil || sess == nil {
		return
	}
	if m.mutationEventsOutboxed() {
		return
	}
	ev := wire.SessionEvent{
		ID:        sess.ID,
		ProjectID: sess.ProjectID,
		Action:    wire.SessionEventActionUpdated,
		Title:     sess.Title,
		Status:    sess.Status,
	}
	key := events.PublishKey{Project: strings.TrimSpace(sess.ProjectID)}
	_ = m.events.Hub.Publish(ctx, wire.EventTopicSession, key, ev)
	m.events.PublishAttention(ctx)
}
