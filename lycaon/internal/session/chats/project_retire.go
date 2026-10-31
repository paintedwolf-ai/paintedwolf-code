package chats

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// RetireForProjectDelete deletes every project chat, including worktree-bound ones.
func (m *Service) RetireForProjectDelete(ctx context.Context, projectID string, progress func(int, int)) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("session manager not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("project_id required")
	}
	ctx = bgprocess.WithTerminationReason(ctx, bgprocess.TerminationProjectDeleted)
	plan, err := planProjectRetirement(ctx, m.store, projectID)
	if err != nil {
		return err
	}
	completed := 0
	report := func() {
		if progress != nil {
			progress(completed, plan.total)
		}
	}
	report()
	for _, root := range plan.roots {
		if err := m.retireSessionForProjectDelete(ctx, root, func() { completed++; report() }); err != nil {
			return err
		}
	}
	if m.events != nil && !m.store.MutationEventsOutboxed() {
		m.events.PublishAttention(ctx)
	}
	return nil
}

// retireSessionForProjectDelete releases one root tree; retired reports each
// chat whose runtime is gone, so a single large tree still shows movement.
func (m *Service) retireSessionForProjectDelete(
	ctx context.Context,
	root retirementRoot,
	retired func(),
) error {
	sess, tree := root.session, root.tree
	var deleted []*wire.Session
	if !m.store.MutationEventsOutboxed() {
		for _, id := range tree {
			session, err := m.store.Get(ctx, id)
			if errors.Is(err, store.ErrSessionNotFound) {
				// Already gone since planning; nothing is left to announce.
				continue
			}
			if err != nil {
				return err
			}
			deleted = append(deleted, session)
		}
	}
	lock := m.prompt.Acquire(sess.ID)
	lock.Lock()
	defer lock.Unlock()

	for _, id := range tree {
		if m.workers != nil && strings.TrimSpace(id) == strings.TrimSpace(sess.ID) {
			_ = m.workers.AbortAllWorkers(ctx, id, sess.ProjectID, "project deleted")
		}
		if err := m.DisposeRuntime(ctx, id); err != nil {
			return fmt.Errorf("dispose session runtime: %w", err)
		}
		if m.queue != nil {
			m.queue.Clear(id)
		}
		retired()
	}
	if err := m.store.Delete(ctx, sess.ID); err != nil && !errors.Is(err, store.ErrSessionNotFound) {
		return err
	}
	m.removeScratch(ctx, tree)
	for _, id := range tree {
		m.Gate.Forget(id)
	}
	if m.events != nil && !m.store.MutationEventsOutboxed() {
		for _, gone := range deleted {
			m.publishSessionDeleted(ctx, gone)
		}
	}
	return nil
}
