package gitadmin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGitWorktreeView(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(chi.URLParam(r, "id"))
	scope, ok := s.resolveWorktreeScope(w, r, sessionID)
	if !ok {
		return
	}
	if !scope.bound || scope.binding == nil {
		httpio.WriteJSON(w, http.StatusOK, unboundWorktreeView(scope.sess.ID))
		return
	}
	mgr := s.git
	view, err := s.buildWorktreeView(r.Context(), mgr, scope.binding, scope.repos)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, view)
}

func unboundWorktreeView(sessionID string) wire.GitWorktreeView {
	return wire.GitWorktreeView{
		Bound:     false,
		SessionID: sessionID,
	}
}

func (s *Handler) buildWorktreeView(ctx context.Context, mgr *git.Manager, b *store.WorktreeBinding, repos []git.RepoRef) (wire.GitWorktreeView, error) {
	view := wire.GitWorktreeView{
		Bound:      true,
		SessionID:  b.SessionID,
		RepoID:     b.RepoID,
		Branch:     b.Branch,
		BaseBranch: b.BaseBranch,
		State:      "stale",
	}
	if !b.CreatedAt.IsZero() {
		view.CreatedAt = b.CreatedAt.UTC().Format(time.RFC3339)
	}
	for _, repo := range repos {
		if repo.ID == b.RepoID {
			view.Label = repo.Label
			break
		}
	}
	binding := project.Binding{Toplevel: b.Toplevel, WorktreePath: b.WorktreePath, Branch: b.Branch, BaseBranch: b.BaseBranch}
	if mgr.ValidateWorktree(ctx, binding.Toplevel, binding.WorktreePath, binding.Branch) != nil {
		view.Path = b.WorktreePath
		return view, nil //nolint:nilerr // Validation failure is represented by the stale wire state.
	}
	view.State = "ready"
	st, err := mgr.Status(ctx, b.WorktreePath)
	if err != nil {
		return wire.GitWorktreeView{}, err
	}
	view.Dirty = st.Dirty
	baseExists, err := mgr.LocalBranchExists(ctx, b.Toplevel, b.BaseBranch)
	if err != nil {
		return wire.GitWorktreeView{}, err
	}
	if baseExists {
		ahead, behind, aheadErr := mgr.AheadBehindRefs(ctx, b.WorktreePath, b.BaseBranch, b.Branch)
		if aheadErr != nil {
			return wire.GitWorktreeView{}, aheadErr
		}
		view.AheadOfBase = ahead
		view.BehindBase = behind
	} else {
		view.LandBlockedReason = "base_branch_missing"
	}
	return view, nil
}
