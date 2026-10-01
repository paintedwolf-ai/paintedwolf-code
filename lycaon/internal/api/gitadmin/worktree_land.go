package gitadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/git"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGitWorktreeLand(w http.ResponseWriter, r *http.Request) {
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
	ahead, err := mgr.LandWorktree(r.Context(), git.WorktreeLandRequest{
		BaseDir: b.Toplevel, BaseBranch: b.BaseBranch,
		WorktreePath: b.WorktreePath, Branch: b.Branch,
	})
	if err != nil {
		var blocked *git.WorktreeLandError
		if errors.As(err, &blocked) {
			switch blocked.Reason {
			case "worktree_stale":
				s.responses.Fail(w, wire.ApiErrorCodeWorktreeStale, "This chat's worktree is missing or invalid. Use Return to project folder in the Git tab to continue.")
			case "worktree_dirty":
				s.responses.Fail(w, wire.ApiErrorCodeWorktreeDirty, "The worktree has uncommitted changes. Commit or discard them first.")
			default:
				s.writeLandBlocked(w, b.BaseBranch, blocked.Reason, blocked.CurrentBranch, nil)
			}
			return
		}
		var conflict *git.MergeConflictError
		if errors.As(err, &conflict) {
			s.writeLandBlocked(w, b.BaseBranch, "conflict", "", conflict.Paths)
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	if ahead == 0 {
		httpio.WriteJSON(w, http.StatusOK, wire.GitWorktreeLandResult{
			Landed:     false,
			BaseBranch: b.BaseBranch,
			Commits:    0,
			Reason:     "nothing_to_land",
			Conflicts:  []string{},
		})
		return
	}

	httpio.WriteJSON(w, http.StatusOK, wire.GitWorktreeLandResult{
		Landed:     true,
		BaseBranch: b.BaseBranch,
		Commits:    ahead,
		Reason:     "",
		Conflicts:  []string{},
	})
}

// writeLandBlocked answers a land that did not happen; current names the
// checked-out branch when it is not the chat's, conflicts the colliding paths.
func (s *Handler) writeLandBlocked(w http.ResponseWriter, baseBranch, reason, current string, conflicts []string) {
	ctx := map[string]any{
		"reason":      reason,
		"base_branch": baseBranch,
		"landed":      false,
		"commits":     0,
		"conflicts":   append([]string{}, conflicts...),
	}
	if current != "" {
		ctx["current"] = current
	}
	s.responses.FailDetails(w, wire.ApiErrorCodeWorktreeLandBlocked, ctx, "worktree land blocked")
}
