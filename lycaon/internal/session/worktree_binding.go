package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrSessionWorktreeStale marks an invalid session checkout binding.
var ErrSessionWorktreeStale error = noticeerr.NewSentinel("this chat's worktree is missing or invalid", api.NoticeCodeWorktreeStale)

// worktreeBindingFor reads the session's worktree binding through the store port.
// ok=false with err=nil is the only unbound state; a store read failure is returned.
func (m *Manager) worktreeBindingFor(ctx context.Context, sess *api.Session) (project.Binding, bool, error) {
	if m == nil || m.store == nil || sess == nil {
		return project.Binding{}, false, nil
	}
	bindingSessionID := strings.TrimSpace(sess.ID)
	if sess.IsWorkerChild() {
		bindingSessionID = RootSessionID(ctx, m.store, bindingSessionID)
	}
	row, ok, err := m.store.GetWorktreeBinding(ctx, bindingSessionID)
	if err != nil {
		return project.Binding{}, false, err
	}
	if !ok || row == nil {
		return project.Binding{}, false, nil
	}
	return project.Binding{
		ID:           row.WorktreeID,
		Toplevel:     row.Toplevel,
		WorktreePath: row.WorktreePath,
		Branch:       row.Branch,
		BaseBranch:   row.BaseBranch,
	}, true, nil
}

// CheckWorktreeReady returns ErrSessionWorktreeStale when the session is bound
// and its registered checkout is invalid. Unbound sessions and ready bindings succeed.
func (m *Manager) CheckWorktreeReady(ctx context.Context, sessionID string) error {
	if m == nil || m.store == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	b, bound, err := m.worktreeBindingFor(ctx, sess)
	if err != nil {
		return err
	}
	if !bound {
		return nil
	}
	if err := git.NewManager().ValidateWorktree(ctx, b.Toplevel, b.WorktreePath, b.Branch); err != nil {
		return ErrSessionWorktreeStale
	}
	return nil
}
