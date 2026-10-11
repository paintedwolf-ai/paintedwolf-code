package submissionstate

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	Get(context.Context, string) (*api.Session, error)
	ListUnsettledUserPromptSubmissionIDs(context.Context, string) ([]string, error)
	LastTurnMessageContent(context.Context, string) (string, error)
}
type Queue interface{ AwaitsPerson(string, string) bool }
type Service struct {
	store     Store
	queue     Queue
	publisher *events.Publisher
	begun     begunSubmissions
}

func New(store Store, queue Queue) *Service                 { return &Service{store: store, queue: queue} }
func (s *Service) SetPublisher(publisher *events.Publisher) { s.publisher = publisher }
func (s *Service) Started(submissionID string) bool         { return s.begun.has(submissionID) }
func (s *Service) Forget(submissionID string)               { s.begun.forget(submissionID) }
func (s *Service) lastMessage(ctx context.Context, sessionID string) string {
	content, err := s.store.LastTurnMessageContent(ctx, sessionID)
	if err != nil {
		return ""
	}
	return content
}
func (s *Dispatch) Began() bool { return s.began.Load() }

// PromptPending reports whether the session holds an admitted human prompt the
// host will run without further human action and whose turn has not begun.
func (m *Service) PromptPending(ctx context.Context, sessionID string) bool {
	if m == nil || m.store == nil {
		return false
	}
	unsettled, err := m.store.ListUnsettledUserPromptSubmissionIDs(ctx, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "read pending prompts", "session_id", sessionID, "error", err)
		return false
	}
	return m.PromptPendingAmong(sessionID, unsettled)
}

// PromptPendingAmong decides from a session's queued and running human prompts.
// A prompt stops pending once its turn begins, or while it sits in a held draft.
func (m *Service) PromptPendingAmong(sessionID string, unsettled []string) bool {
	if m == nil {
		return false
	}
	for _, id := range unsettled {
		if m.begun.has(id) {
			continue
		}
		if m.queue != nil && m.queue.AwaitsPerson(sessionID, id) {
			continue
		}
		return true
	}
	return false
}

// SessionState reads the status and prompt_pending a session event carries.
func (m *Service) SessionState(ctx context.Context, sessionID string) (api.SessionStatus, bool, error) {
	if m == nil || m.store == nil {
		return "", false, fmt.Errorf("session manager unavailable")
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return "", false, err
	}
	return sess.Status, m.PromptPending(ctx, sessionID), nil
}

// Publish republishes the session after a pending-prompt change
// that no lifecycle edge reports. Status is read after the revision is stamped.
func (m *Service) Publish(ctx context.Context, sessionID string) {
	if m == nil || m.publisher == nil || m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return
	}
	preview := m.lastMessage(ctx, sessionID)
	m.publisher.PublishSession(ctx, strings.TrimSpace(sess.ProjectID), sessionID, "", preview)
}

// begunSubmissions holds claimed submissions whose turn has begun, until their
// receipts close.
type begunSubmissions struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (b *begunSubmissions) add(ids []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ids == nil {
		b.ids = make(map[string]struct{})
	}
	for _, id := range ids {
		b.ids[id] = struct{}{}
	}
}

func (b *begunSubmissions) has(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.ids[id]
	return ok
}

func (b *begunSubmissions) forget(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.ids, id)
}

// submissionDispatch is one run of claimed submissions. A host continuation
// inside the run begins their turn too.
type Dispatch struct {
	ids   []string
	began atomic.Bool
}

type submissionDispatchContextKey struct{}

func WithDispatch(ctx context.Context, ids ...string) (context.Context, *Dispatch) {
	dispatch := &Dispatch{ids: ids}
	return context.WithValue(ctx, submissionDispatchContextKey{}, dispatch), dispatch
}

// MarkTurnBegan records that the session went busy for the dispatch in ctx.
func (m *Service) MarkTurnBegan(ctx context.Context) {
	dispatch, ok := ctx.Value(submissionDispatchContextKey{}).(*Dispatch)
	if !ok || dispatch.began.Swap(true) {
		return
	}
	m.begun.add(dispatch.ids)
}
