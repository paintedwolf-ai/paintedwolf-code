package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/pkg/api"
)

// BoardGitWorktreeFunc returns a SnapshotBuilder.Worktree dependency that reads
// the session binding and probes the worktree through the git manager.
func (m *Manager) BoardGitWorktreeFunc(gm git.GitManager) func(ctx context.Context, sessionID string) *api.BoardGitWorktree {
	return func(ctx context.Context, sessionID string) *api.BoardGitWorktree {
		if m == nil || m.store == nil || gm == nil {
			return nil
		}
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return nil
		}
		sess, err := m.store.Get(ctx, sessionID)
		if err != nil || sess == nil {
			return nil
		}
		b, bound, err := m.worktreeBindingFor(ctx, sess)
		concrete, concreteOK := gm.(*git.Manager)
		if err != nil || !bound || !concreteOK || concrete.ValidateWorktree(ctx, b.Toplevel, b.WorktreePath, b.Branch) != nil {
			return nil
		}
		fact := &api.BoardGitWorktree{
			Branch:     b.Branch,
			BaseBranch: b.BaseBranch,
		}
		if st, err := gm.Status(ctx, b.WorktreePath); err == nil && st != nil {
			fact.Dirty = st.Dirty
		}
		if ahead, behind, err := concrete.AheadBehindRefs(ctx, b.WorktreePath, b.BaseBranch, b.Branch); err == nil {
			fact.AheadOfBase = ahead
			fact.BehindBase = behind
		}
		return fact
	}
}
