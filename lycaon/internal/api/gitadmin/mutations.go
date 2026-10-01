package gitadmin

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// gitBranchListMax caps the branch picker list.
const gitBranchListMax = 200

// gitCommandFailed answers a refused git command with host copy; git's own
// output stays in the log because it can carry remote URLs and paths.
func (s *Handler) gitCommandFailed(w http.ResponseWriter, r *http.Request, code wire.ApiErrorCode, message string, err error) {
	s.responses.Logger.WarnContext(r.Context(), "git command failed", "code", code, "path", r.URL.Path, "err", err)
	s.responses.Fail(w, code, message)
}

// HandleGitBranches lists the repository's local branches for the Git tab picker.
func (s *Handler) HandleGitBranches(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	repo, _, ok := s.requireGitRepoQuery(w, r)
	if !ok {
		return
	}
	branches, err := mgr.Branches(r.Context(), repo.Toplevel, gitBranchListMax)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out := make([]wire.GitBranchEntry, 0, len(branches))
	for _, b := range branches {
		out = append(out, wire.GitBranchEntry{Name: b.Name, Current: b.Current})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.GitBranchesView{Branches: out})
}

// HandleGitCheckout switches to a branch, creating it first when create is set.
func (s *Handler) HandleGitCheckout(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	var req wire.GitCheckoutRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if sessionID := strings.TrimSpace(r.URL.Query().Get("session_id")); sessionID != "" {
		_, bound, err := s.SessionStore.GetWorktreeBinding(r.Context(), sessionID)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if bound {
			s.responses.Fail(w, wire.ApiErrorCodeGitCheckoutFailed, "This chat stays on its worktree branch. Return to the project folder before switching branches.")
			return
		}
	}
	repo, _, ok := s.requireGitRepoQuery(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Branch) == "" {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "branch"}, "branch is required")
		return
	}
	var err error
	if req.Create {
		err = mgr.CreateBranch(r.Context(), repo.Toplevel, req.Branch)
	} else {
		err = mgr.Checkout(r.Context(), repo.Toplevel, req.Branch)
	}
	if err != nil {
		s.gitCommandFailed(w, r, wire.ApiErrorCodeGitCheckoutFailed, "Git could not switch branches.", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.gitMutationResult(r.Context(), repo))
}

// HandleGitDiscard reverts the working tree to a clean state.
func (s *Handler) HandleGitDiscard(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	repo, _, ok := s.requireGitRepoQuery(w, r)
	if !ok {
		return
	}
	if err := mgr.DiscardAll(r.Context(), repo.Toplevel); err != nil {
		s.gitCommandFailed(w, r, wire.ApiErrorCodeGitDiscardFailed, "Git could not discard the changes.", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.gitMutationResult(r.Context(), repo))
}

// HandleGitPush publishes the current branch to its upstream.
func (s *Handler) HandleGitPush(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	repo, _, ok := s.requireGitRepoQuery(w, r)
	if !ok {
		return
	}
	if err := mgr.Push(r.Context(), repo.Toplevel); err != nil {
		s.gitCommandFailed(w, r, wire.ApiErrorCodeGitPushFailed, "Git could not push to the upstream branch.", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.gitMutationResult(r.Context(), repo))
}

// HandleGitPull fast-forwards the current branch from its upstream.
func (s *Handler) HandleGitPull(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	repo, _, ok := s.requireGitRepoQuery(w, r)
	if !ok {
		return
	}
	if err := mgr.Pull(r.Context(), repo.Toplevel); err != nil {
		s.gitCommandFailed(w, r, wire.ApiErrorCodeGitPullFailed, "Git could not pull from the upstream branch.", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.gitMutationResult(r.Context(), repo))
}

// HandleCreateGitRepo initializes a new git repository in the named project root.
func (s *Handler) HandleCreateGitRepo(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	var req wire.GitInitRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	rootID := strings.TrimSpace(req.RootID)
	if rootID == "" {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "root_id"}, "root_id is required")
		return
	}
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	var root *project.Root
	for i := range p.Roots {
		if p.Roots[i].ID == rootID {
			root = &p.Roots[i]
			break
		}
	}
	if root == nil {
		s.responses.Fail(w, wire.ApiErrorCodeRootNotFound, "root not found")
		return
	}
	if err := mgr.Init(r.Context(), root.Path); err != nil {
		s.gitCommandFailed(w, r, wire.ApiErrorCodeGitInitFailed, "Git could not create a repository in this folder.", err)
		return
	}
	if s.RepoSetCache != nil {
		s.RepoSetCache.Invalidate(p.ID)
	}
	roots := project.RootRefsFrom(p)
	repos, err := s.LoadOrderedRepos(r.Context(), p, roots, "", "")
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	for _, repo := range repos {
		if !repo.Available {
			continue
		}
		for _, id := range repo.RootIDs {
			if id == rootID {
				entry := wire.GitRepoEntry{
					RepoID:    repo.ID,
					Label:     repo.Label,
					RootIDs:   append([]string(nil), repo.RootIDs...),
					Available: repo.Available,
				}
				s.populateGitRepoSummary(r.Context(), &entry, repo)
				httpio.WriteJSON(w, http.StatusCreated, entry)
				return
			}
		}
	}
	// Fail-soft: init succeeded but discovery has not yet surfaced the repo.
	entry := wire.GitRepoEntry{
		RepoID:        rootID,
		Label:         filepath.Base(root.Path),
		RootIDs:       []string{rootID},
		Available:     true,
		StatusPending: true,
	}
	httpio.WriteJSON(w, http.StatusCreated, entry)
}

// HandleGitCommit stages the requested paths (all changes when empty) and commits them.
func (s *Handler) HandleGitCommit(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	var req wire.GitCommitRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	repo, _, ok := s.requireGitRepoQuery(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "message"}, "commit message is required")
		return
	}
	if _, err := mgr.Commit(r.Context(), repo.Toplevel, git.GitCommitOpts{Message: req.Message, Paths: req.Paths}); err != nil {
		s.gitCommandFailed(w, r, wire.ApiErrorCodeGitCommitFailed, "Git could not create the commit.", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.gitMutationResult(r.Context(), repo))
}

// HandleGitStash saves the working tree (including untracked files) to a new stash entry.
func (s *Handler) HandleGitStash(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	var req wire.GitStashRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	repo, _, ok := s.requireGitRepoQuery(w, r)
	if !ok {
		return
	}
	if err := mgr.Stash(r.Context(), repo.Toplevel, req.Message); err != nil {
		s.gitCommandFailed(w, r, wire.ApiErrorCodeGitStashFailed, "Git could not stash the changes.", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.gitMutationResult(r.Context(), repo))
}
