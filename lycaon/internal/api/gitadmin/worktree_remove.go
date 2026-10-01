package gitadmin

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleDeleteGitWorktree(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(chi.URLParam(r, "id"))
	mgr := s.git
	unlock, ok := s.acquireIdleMutation(w, sessionID)
	if !ok {
		return
	}
	defer unlock()

	scope, ok := s.resolveWorktreeScope(w, r, sessionID)
	if !ok {
		return
	}
	if !scope.bound || scope.binding == nil {
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeNotBound, "This chat has no worktree.")
		return
	}
	b := scope.binding
	ready := mgr.ValidateWorktree(r.Context(), b.Toplevel, b.WorktreePath, b.Branch) == nil
	if ready {
		st, err := mgr.Status(r.Context(), b.WorktreePath)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if st.Dirty {
			s.responses.Fail(w, wire.ApiErrorCodeWorktreeDirty, "The worktree has uncommitted changes. Commit or discard them first.")
			return
		}
		before, err := mgr.RepoConfigFingerprint(r.Context(), b.Toplevel)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if err := mgr.RemoveWorktree(r.Context(), b.Toplevel, b.WorktreePath); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		after, err := mgr.RepoConfigFingerprint(r.Context(), b.Toplevel)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if after != before {
			s.responses.InternalError(w, r, errors.New("repository config changed during worktree remove"))
			return
		}
	} else if _, statErr := os.Stat(b.WorktreePath); errors.Is(statErr, os.ErrNotExist) {
		before, err := mgr.RepoConfigFingerprint(r.Context(), b.Toplevel)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if err := mgr.PruneWorktrees(r.Context(), b.Toplevel); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		after, err := mgr.RepoConfigFingerprint(r.Context(), b.Toplevel)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if after != before {
			s.responses.InternalError(w, r, errors.New("repository config changed during worktree prune"))
			return
		}
	} else {
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeStale, "This chat's worktree is invalid. Repair or remove the checkout at the shown path before returning to the project folder.")
		return
	}

	if err := s.SessionStore.DeleteWorktreeBinding(r.Context(), scope.sess.ID); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
