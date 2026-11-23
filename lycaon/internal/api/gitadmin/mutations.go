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
	target, ok := s.requireGitCheckoutTarget(w, r)
	if !ok {
		return
	}
	branches, err := mgr.Branches(r.Context(), target.Repo.Toplevel, gitBranchListMax)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out := make([]wire.GitBranchEntry, 0, len(branches))
	for _, b := range branches {
		out = append(out, wire.GitBranchEntry{Name: b.Name, Current: b.Current})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.GitBranchesView{
		WorktreeID: target.Checkout.ID,
		Branches:   out,
	})
}

// HandleGitCheckout switches to a branch, creating it first when create is set.
func (s *Handler) HandleGitCheckout(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	var req wire.GitCheckoutRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !r.URL.Query().Has("worktree_id") {
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
	}
	target, ok := s.requireGitCheckoutTarget(w, r)
	if !ok {
		return
	}
	if target.BoundToSession || target.BoundToAny {
		s.responses.Fail(w, wire.ApiErrorCodeGitCheckoutFailed, "This chat stays on its worktree branch. Return to the project folder before switching branches.")
		return
	}
	if strings.TrimSpace(req.Branch) == "" {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "branch"}, "branch is required")
		return
	}
	var expectedHead *string
	if strings.TrimSpace(req.ExpectedHead) != "" {
		v := strings.TrimSpace(req.ExpectedHead)
		expectedHead = &v
	}
	ctx := s.lockedMutationContext(r.Context(), target, expectedHead, 0)
	var err error
	if req.Create {
		err = mgr.CreateBranch(ctx, target.Repo.Toplevel, req.Branch)
	} else {
		err = mgr.Checkout(ctx, target.Repo.Toplevel, req.Branch)
	}
	if err != nil {
		s.writeMutationGuardOrCommandError(w, r, wire.ApiErrorCodeGitCheckoutFailed, "Git could not switch branches.", err)
		return
	}
	s.ScheduleWorktreeRefresh(r.Context(), target.BaseProject.ID)
	httpio.WriteJSON(w, http.StatusOK, s.gitMutationResult(r.Context(), target.Repo, target.Checkout.ID))
}

// HandleGitDiscard reverts the working tree to a clean state.
func (s *Handler) HandleGitDiscard(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	target, ok := s.requireGitCheckoutTarget(w, r)
	if !ok {
		return
	}
	var expectedHead *string
	if r.URL.Query().Has("expected_head") {
		v := strings.TrimSpace(r.URL.Query().Get("expected_head"))
		expectedHead = &v
	}
	expectedRevision := uint64(0)
	if parsed, present, err := httpio.OptionalUint64Query(r, "expected_revision", 1); err != nil {
		s.responses.InvalidQuery(w, err)
		return
	} else if present {
		expectedRevision = parsed
	}
	ctx := s.lockedMutationContext(r.Context(), target, expectedHead, expectedRevision)
	if err := mgr.DiscardAll(ctx, target.Repo.Toplevel); err != nil {
		s.writeMutationGuardOrCommandError(w, r, wire.ApiErrorCodeGitDiscardFailed, "Git could not discard the changes.", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.gitMutationResult(r.Context(), target.Repo, target.Checkout.ID))
}

// HandleGitPush publishes the current branch to its upstream or an explicit remote/ref.
func (s *Handler) HandleGitPush(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	target, ok := s.requireGitCheckoutTarget(w, r)
	if !ok {
		return
	}
	remote := strings.TrimSpace(r.URL.Query().Get("remote"))
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if remote == "" && ref != "" {
		s.responses.InvalidQueryParam(w, "remote", "is required when ref is specified")
		return
	}
	ctx := s.lockedMutationContext(r.Context(), target, nil, 0)
	if err := mgr.PushRef(ctx, target.Repo.Toplevel, remote, ref); err != nil {
		s.writeMutationGuardOrCommandError(w, r, wire.ApiErrorCodeGitPushFailed, "Git could not push to the upstream branch.", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.gitMutationResult(r.Context(), target.Repo, target.Checkout.ID))
}

// HandleGitPull fast-forwards the current branch from its upstream or an explicit remote/ref.
func (s *Handler) HandleGitPull(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	target, ok := s.requireGitCheckoutTarget(w, r)
	if !ok {
		return
	}
	remote := strings.TrimSpace(r.URL.Query().Get("remote"))
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if remote == "" && ref != "" {
		s.responses.InvalidQueryParam(w, "remote", "is required when ref is specified")
		return
	}
	ctx := s.lockedMutationContext(r.Context(), target, nil, 0)
	if err := mgr.PullRef(ctx, target.Repo.Toplevel, remote, ref); err != nil {
		s.writeMutationGuardOrCommandError(w, r, wire.ApiErrorCodeGitPullFailed, "Git could not pull from the upstream branch.", err)
		return
	}
	s.ScheduleWorktreeRefresh(r.Context(), target.BaseProject.ID)
	httpio.WriteJSON(w, http.StatusOK, s.gitMutationResult(r.Context(), target.Repo, target.Checkout.ID))
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
	for _, repo := range s.LoadOrderedRepos(r.Context(), p, p.SourceRoots(), "", nil) {
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
	target, ok := s.requireGitCheckoutTarget(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "message"}, "commit message is required")
		return
	}
	var expectedHead *string
	if strings.TrimSpace(req.ExpectedHead) != "" {
		v := strings.TrimSpace(req.ExpectedHead)
		expectedHead = &v
	}
	ctx := s.lockedMutationContext(r.Context(), target, expectedHead, req.ExpectedRevision)
	if _, err := mgr.Commit(ctx, target.Repo.Toplevel, git.GitCommitOpts{
		Message:    req.Message,
		Paths:      req.Paths,
		StagedOnly: req.StagedOnly,
	}); err != nil {
		s.writeMutationGuardOrCommandError(w, r, wire.ApiErrorCodeGitCommitFailed, "Git could not create the commit.", err)
		return
	}
	s.ScheduleWorktreeRefresh(r.Context(), target.BaseProject.ID)
	httpio.WriteJSON(w, http.StatusOK, s.gitMutationResult(r.Context(), target.Repo, target.Checkout.ID))
}

// HandleGitStash saves, applies, pops, or drops stash entries in the target checkout.
func (s *Handler) HandleGitStash(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	var req wire.GitStashRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	target, ok := s.requireGitCheckoutTarget(w, r)
	if !ok {
		return
	}
	var expectedHead *string
	if strings.TrimSpace(req.ExpectedHead) != "" {
		v := strings.TrimSpace(req.ExpectedHead)
		expectedHead = &v
	}
	ctx := s.lockedMutationContext(r.Context(), target, expectedHead, 0)
	action := strings.TrimSpace(req.Action)
	if action == "" {
		action = "push"
	}
	switch action {
	case "push":
		if err := mgr.Stash(ctx, target.Repo.Toplevel, req.Message); err != nil {
			s.writeMutationGuardOrCommandError(w, r, wire.ApiErrorCodeGitStashFailed, "Git could not stash the changes.", err)
			return
		}
	case "apply", "drop":
		res, err := mgr.Operate(ctx, target.Repo.Toplevel, git.OperationRequest{
			Kind:           "stash",
			Action:         action,
			Ref:            strings.TrimSpace(req.Ref),
			ReinstateIndex: req.ReinstateIndex,
		})
		if err != nil || res.Status == "failed" {
			s.writeMutationGuardOrCommandError(w, r, wire.ApiErrorCodeGitStashFailed, "Git could not update the stash.", err)
			return
		}
	case "pop":
		ref := strings.TrimSpace(req.Ref)
		if ref == "" {
			ref = "stash@{0}"
		}
		applied, err := mgr.Operate(ctx, target.Repo.Toplevel, git.OperationRequest{
			Kind:           "stash",
			Action:         "apply",
			Ref:            ref,
			ReinstateIndex: req.ReinstateIndex,
		})
		if err != nil || applied.Status == "failed" {
			s.writeMutationGuardOrCommandError(w, r, wire.ApiErrorCodeGitStashFailed, "Git could not pop the stash.", err)
			return
		}
		if dropped, dErr := mgr.Operate(ctx, target.Repo.Toplevel, git.OperationRequest{
			Kind:   "stash",
			Action: "drop",
			Ref:    ref,
		}); dErr != nil || dropped.Status == "failed" {
			s.writeMutationGuardOrCommandError(w, r, wire.ApiErrorCodeGitStashFailed, "Git applied the stash, then could not drop the stash entry.", dErr)
			return
		}
	default:
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "action"}, "action must be push, apply, pop, or drop")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.gitMutationResult(r.Context(), target.Repo, target.Checkout.ID))
}
