package gitadmin

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGitWorktreeBind(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(chi.URLParam(r, "id"))
	var req wire.GitWorktreeBindRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.RepoID) == "" {
		s.responses.InvalidField(w, "repo_id", "is required")
		return
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		s.responses.InvalidField(w, "branch", "is required")
		return
	}
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
	if scope.bound {
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeAlreadyBound, "This chat already has a worktree.")
		return
	}

	repoID := strings.TrimSpace(req.RepoID)
	var repo git.RepoRef
	found := false
	for _, candidate := range scope.repos {
		if candidate.ID == repoID && candidate.Available {
			repo = candidate
			found = true
			break
		}
	}
	if !found {
		s.responses.Fail(w, wire.ApiErrorCodeGitRepoNotFound, GitRepoNotFoundMsg)
		return
	}

	baseBranch, err := mgr.Branch(r.Context(), repo.Toplevel)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if strings.TrimSpace(baseBranch) == "" {
		s.responses.FailDetails(w, wire.ApiErrorCodeWorktreeLandBlocked, map[string]any{
			"reason": "detached_head",
		}, "repository HEAD is detached")
		return
	}

	if err := gitargv.ValidateRefArg(branch); err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "branch"}, "invalid branch name")
		return
	}
	exists, err := mgr.LocalBranchExists(r.Context(), repo.Toplevel, branch)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if exists {
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeBranchExists, "A branch with that name already exists.")
		return
	}

	projectDir := project.PrimaryRootPath(scope.project)
	if projectDir == "" {
		projectDir = repo.Toplevel
	}
	worktreeRoot := enginepaths.SessionWorktreesRootUnder(scope.dataRoot)
	path := enginepaths.SessionWorktreeDir(worktreeRoot, projectDir, scope.sess.ID)
	if _, err := os.Stat(path); err == nil {
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeAlreadyBound, "This chat already has a worktree.")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.responses.InternalError(w, r, err)
		return
	}

	before, err := mgr.RepoConfigFingerprint(r.Context(), repo.Toplevel)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if err := os.MkdirAll(enginepaths.ProjectWorktreeDir(worktreeRoot, projectDir), 0o700); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	row := store.WorktreeBinding{
		SessionID:    scope.sess.ID,
		ProjectID:    scope.project.ID,
		RepoID:       repo.ID,
		Toplevel:     repo.Toplevel,
		WorktreePath: path,
		Branch:       branch,
		BaseBranch:   baseBranch,
		CreatedAt:    time.Now().UTC(),
	}
	// Persist the binding first so partial creation remains recoverable.
	if err := s.SessionStore.PutWorktreeBinding(r.Context(), row); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if err := mgr.AddWorktree(r.Context(), repo.Toplevel, path, branch, baseBranch); err != nil {
		branchExists, branchErr := mgr.LocalBranchExists(r.Context(), repo.Toplevel, branch)
		_, statErr := os.Stat(path)
		createdNothing := branchErr == nil && !branchExists && errors.Is(statErr, os.ErrNotExist)
		if createdNothing {
			if deleteErr := s.SessionStore.DeleteWorktreeBinding(r.Context(), scope.sess.ID); deleteErr != nil {
				s.responses.Logger.ErrorContext(r.Context(), "worktree bind row rollback failed", "session_id", scope.sess.ID, "cause", err, "rollback_error", deleteErr)
			}
		} else {
			s.responses.Logger.ErrorContext(r.Context(), "worktree bind left a durable binding after partial git create", "session_id", scope.sess.ID, "cause", err, "branch_probe_error", branchErr, "path_probe_error", statErr)
		}
		s.responses.InternalError(w, r, err)
		return
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // Private checkout directory.
		s.responses.InternalError(w, r, err)
		return
	}
	after, err := mgr.RepoConfigFingerprint(r.Context(), repo.Toplevel)
	if err != nil || after != before {
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		s.responses.InternalError(w, r, errors.New("repository config changed during worktree create"))
		return
	}

	view, err := s.buildWorktreeView(r.Context(), mgr, &row, scope.repos)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, view)
}
