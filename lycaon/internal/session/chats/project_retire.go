package chats

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// RetireForProjectDelete deletes every project chat, including worktree-bound ones.
func (m *Service) RetireForProjectDelete(ctx context.Context, projectID string) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("session manager not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("project_id required")
	}
	sessions, err := m.store.List(ctx)
	if err != nil {
		return err
	}
	mine := sessionsForProject(sessions, projectID)
	sort.SliceStable(mine, func(i, j int) bool {
		a, b := mine[i], mine[j]
		aRoot := strings.TrimSpace(a.ParentSessionID) == ""
		bRoot := strings.TrimSpace(b.ParentSessionID) == ""
		if aRoot != bRoot {
			return aRoot
		}
		return a.ID < b.ID
	})
	seen := make(map[string]struct{}, len(mine))
	for _, sess := range mine {
		if sess == nil {
			continue
		}
		if _, ok := seen[sess.ID]; ok {
			continue
		}
		if err := m.retireSessionForProjectDelete(ctx, sess, mine, seen); err != nil {
			return err
		}
	}
	if m.events != nil && !m.store.MutationEventsOutboxed() {
		m.events.PublishAttention(ctx)
	}
	return nil
}

func (m *Service) retireSessionForProjectDelete(
	ctx context.Context,
	sess *wire.Session,
	projectSessions []*wire.Session,
	seen map[string]struct{},
) error {
	lock := m.prompt.Acquire(sess.ID)
	lock.Lock()
	defer lock.Unlock()

	tree := sessionIDsInTree(projectSessions, sess.ID)
	for _, id := range tree {
		seen[id] = struct{}{}
		if m.workers != nil && strings.TrimSpace(id) == strings.TrimSpace(sess.ID) {
			_ = m.workers.AbortAllWorkers(ctx, id, sess.ProjectID, "project deleted")
		}
		if err := m.DisposeRuntime(ctx, id); err != nil {
			return fmt.Errorf("dispose session runtime: %w", err)
		}
	}
	if err := m.store.Delete(ctx, sess.ID); err != nil && !errors.Is(err, store.ErrSessionNotFound) {
		return err
	}
	m.removeScratch(ctx, tree)
	for _, id := range tree {
		m.Gate.Forget(id)
	}
	if m.events != nil && !m.store.MutationEventsOutboxed() {
		for _, id := range tree {
			gone := sessionByID(projectSessions, id)
			if gone == nil {
				continue
			}
			m.publishSessionDeleted(ctx, gone)
		}
	}
	return nil
}

func sessionsForProject(sessions []*wire.Session, projectID string) []*wire.Session {
	out := make([]*wire.Session, 0, len(sessions))
	for _, sess := range sessions {
		if sess == nil || strings.TrimSpace(sess.ProjectID) != projectID {
			continue
		}
		out = append(out, sess)
	}
	return out
}

func sessionByID(sessions []*wire.Session, id string) *wire.Session {
	for _, sess := range sessions {
		if sess != nil && sess.ID == id {
			return sess
		}
	}
	return nil
}

func sessionIDsInTree(sessions []*wire.Session, rootID string) []string {
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return nil
	}
	children := map[string][]string{}
	for _, sess := range sessions {
		if sess == nil {
			continue
		}
		parent := strings.TrimSpace(sess.ParentSessionID)
		if parent == "" {
			continue
		}
		children[parent] = append(children[parent], sess.ID)
	}
	out := []string{rootID}
	queue := []string{rootID}
	seen := map[string]struct{}{rootID: {}}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, child := range children[id] {
			if _, ok := seen[child]; ok {
				continue
			}
			seen[child] = struct{}{}
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out
}
