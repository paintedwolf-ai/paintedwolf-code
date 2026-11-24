package gitadmin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	WorktreeNotFoundMsg    = "That checkout is not registered for this repository."
	WorktreeUnavailableMsg = "That checkout is no longer available on disk."
	GitStateChangedMsg     = "This checkout changed since preview. Refresh before retrying."
)

// CheckoutTarget holds a verified repository and checkout projection for one Git request.
type CheckoutTarget struct {
	BaseProject      *project.Project
	ProjectedProject *project.Project
	BaseRepo         git.RepoRef
	Repo             git.RepoRef
	Checkout         worktrees.Checkout
	SessionID        string
	BoundToSession   bool
	BoundToAny       bool
	Explicit         bool
}

// CheckoutAdmitted reports whether a registered checkout path satisfies caller standing admission.
func CheckoutAdmitted(worktreePath string) bool {
	if strings.TrimSpace(worktreePath) == "" {
		return false
	}
	if refused, _ := confine.AttachedWriteRootRefused(worktreePath); refused {
		return false
	}
	return !confine.ControlPlaneWriteDenied(worktreePath)
}

// ResolveCheckoutProject verifies checkoutID on disk and projects baseProject's roots onto it.
func (s *Handler) ResolveCheckoutProject(
	ctx context.Context,
	baseProject *project.Project,
	checkoutID string,
) (*project.Project, worktrees.Checkout, wire.ApiErrorCode, string, bool) {
	return resolveCheckoutProject(s, ctx, baseProject, checkoutID)
}

func (s *Handler) requireGitCheckoutTarget(w http.ResponseWriter, r *http.Request) (CheckoutTarget, bool) {
	return resolveGitCheckoutTarget(s, w, r)
}

func (s *Handler) lockedMutationContext(
	ctx context.Context,
	target CheckoutTarget,
	expectedHead *string,
	expectedRevision uint64,
) context.Context {
	return buildLockedMutationContext(s, ctx, target, expectedHead, expectedRevision)
}

func (s *Handler) writeMutationGuardOrCommandError(
	w http.ResponseWriter,
	r *http.Request,
	fallbackCode wire.ApiErrorCode,
	fallbackMessage string,
	err error,
) {
	writeCheckoutMutationError(s, w, r, fallbackCode, fallbackMessage, err)
}

func resolveCheckoutProject(
	s *Handler,
	ctx context.Context,
	baseProject *project.Project,
	checkoutID string,
) (*project.Project, worktrees.Checkout, wire.ApiErrorCode, string, bool) {
	checkoutID = strings.TrimSpace(checkoutID)
	if _, err := uuid.Parse(checkoutID); err != nil {
		return nil, worktrees.Checkout{}, wire.ApiErrorCodeInvalidRequest, "worktree_id must be a valid UUID.", false
	}
	if s == nil || s.Worktrees == nil || baseProject == nil {
		return nil, worktrees.Checkout{}, wire.ApiErrorCodeWorktreeNotFound, WorktreeNotFoundMsg, false
	}
	checkout, err := s.Worktrees.VerifyLive(ctx, baseProject.ID, checkoutID)
	if err != nil {
		if errors.Is(err, worktrees.ErrCheckoutNotFound) {
			return nil, worktrees.Checkout{}, wire.ApiErrorCodeWorktreeNotFound, WorktreeNotFoundMsg, false
		}
		return nil, worktrees.Checkout{}, wire.ApiErrorCodeWorktreeUnavailable, WorktreeUnavailableMsg, false
	}
	if !CheckoutAdmitted(checkout.WorktreePath) {
		return nil, worktrees.Checkout{}, wire.ApiErrorCodeWorktreeUnavailable, WorktreeUnavailableMsg, false
	}
	projected := project.WithWorktree(baseProject, project.Binding{
		ID:           checkout.ID,
		Toplevel:     checkout.Toplevel,
		WorktreePath: checkout.WorktreePath,
		Branch:       checkout.Branch,
		BaseBranch:   checkout.BaseBranch,
	})
	return projected, checkout, "", "", true
}

func resolveGitCheckoutTarget(s *Handler, w http.ResponseWriter, r *http.Request) (CheckoutTarget, bool) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return CheckoutTarget{}, false
	}
	repoID := strings.TrimSpace(chi.URLParam(r, "repo_id"))
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))

	if !r.URL.Query().Has("worktree_id") {
		return resolveImplicitCheckoutTarget(s, w, r, p, repoID, sessionID)
	}

	worktreeID := strings.TrimSpace(r.URL.Query().Get("worktree_id"))
	if _, err := uuid.Parse(worktreeID); err != nil {
		s.responses.InvalidQueryParam(w, "worktree_id", "must be a valid checkout UUID")
		return CheckoutTarget{}, false
	}

	var binding *store.WorktreeBinding
	if sessionID != "" {
		sess, err := s.SessionStore.Get(r.Context(), sessionID)
		if err != nil {
			s.WriteGitReposLoadError(w, r, err)
			return CheckoutTarget{}, false
		}
		if sess == nil || strings.TrimSpace(sess.ProjectID) != strings.TrimSpace(p.ID) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "session not found")
			return CheckoutTarget{}, false
		}
		b, bound, err := s.SessionStore.GetWorktreeBinding(r.Context(), sessionID)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return CheckoutTarget{}, false
		}
		if bound {
			binding = b
		}
	}

	var baseRepo git.RepoRef
	foundBase := false
	for _, repo := range s.LoadOrderedRepos(r.Context(), p, p.SourceRoots(), "", nil) {
		if repo.ID == repoID && repo.Available {
			baseRepo = repo
			foundBase = true
			break
		}
	}
	if !foundBase {
		s.responses.Fail(w, wire.ApiErrorCodeGitRepoNotFound, GitRepoNotFoundMsg)
		return CheckoutTarget{}, false
	}

	projected, checkout, errCode, errMsg, ok := resolveCheckoutProject(s, r.Context(), p, worktreeID)
	if !ok {
		s.responses.Fail(w, errCode, errMsg)
		return CheckoutTarget{}, false
	}
	if checkout.RepoID != baseRepo.ID || canonicalGitPath(checkout.Toplevel) != canonicalGitPath(baseRepo.Toplevel) {
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeNotFound, WorktreeNotFoundMsg)
		return CheckoutTarget{}, false
	}

	projectedRootSet := make(map[string]struct{}, len(projected.Roots))
	for _, root := range projected.Roots {
		projectedRootSet[root.ID] = struct{}{}
	}
	projectedRootIDs := make([]string, 0, len(baseRepo.RootIDs))
	for _, id := range baseRepo.RootIDs {
		if _, ok := projectedRootSet[id]; ok {
			projectedRootIDs = append(projectedRootIDs, id)
		}
	}
	if len(baseRepo.RootIDs) > 0 && len(projectedRootIDs) == 0 {
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeUnavailable, WorktreeUnavailableMsg)
		return CheckoutTarget{}, false
	}

	repo := baseRepo
	repo.Toplevel = canonicalGitPath(checkout.WorktreePath)
	repo.RootIDs = projectedRootIDs

	boundToSession := binding != nil && (binding.WorktreeID == checkout.ID ||
		(binding.RepoID == baseRepo.ID && canonicalGitPath(binding.WorktreePath) == repo.Toplevel))
	boundToAny, err := s.Worktrees.IsBoundToAnySession(r.Context(), p.ID, checkout.ID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return CheckoutTarget{}, false
	}

	return CheckoutTarget{
		BaseProject:      p,
		ProjectedProject: projected,
		BaseRepo:         baseRepo,
		Repo:             repo,
		Checkout:         checkout,
		SessionID:        sessionID,
		BoundToSession:   boundToSession,
		BoundToAny:       boundToSession || boundToAny,
		Explicit:         true,
	}, true
}

func resolveImplicitCheckoutTarget(
	s *Handler,
	w http.ResponseWriter,
	r *http.Request,
	p *project.Project,
	repoID string,
	sessionID string,
) (CheckoutTarget, bool) {
	scope, err := requestscope.ResolveSessionProject(s.SessionStore, r.Context(), p, sessionID)
	if err != nil {
		s.WriteGitReposLoadError(w, r, err)
		return CheckoutTarget{}, false
	}
	var baseRepo git.RepoRef
	for _, candidate := range s.LoadOrderedRepos(r.Context(), p, p.SourceRoots(), "", nil) {
		if candidate.ID == repoID && candidate.Available {
			baseRepo = candidate
			break
		}
	}
	for _, repo := range s.LoadOrderedRepos(r.Context(), p, scope.Project.SourceRoots(), "", scope.Binding) {
		if repo.ID == repoID && repo.Available {
			if baseRepo.ID == "" {
				baseRepo = repo
			}
			boundToSession := scope.Binding != nil && scope.Binding.RepoID == repoID
			checkout := lookupImplicitCheckout(s, r.Context(), p.ID, baseRepo, repo.Toplevel, scope.Binding)
			boundToAny := boundToSession
			if !boundToAny && checkout.ID != "" && s.Worktrees != nil {
				if anyBound, bErr := s.Worktrees.IsBoundToAnySession(r.Context(), p.ID, checkout.ID); bErr == nil {
					boundToAny = anyBound
				}
			}
			return CheckoutTarget{
				BaseProject:      p,
				ProjectedProject: scope.Project,
				BaseRepo:         baseRepo,
				Repo:             repo,
				Checkout:         checkout,
				SessionID:        sessionID,
				BoundToSession:   boundToSession,
				BoundToAny:       boundToAny,
				Explicit:         false,
			}, true
		}
	}
	s.responses.Fail(w, wire.ApiErrorCodeGitRepoNotFound, GitRepoNotFoundMsg)
	return CheckoutTarget{}, false
}

func lookupImplicitCheckout(
	s *Handler,
	ctx context.Context,
	projectID string,
	baseRepo git.RepoRef,
	targetToplevel string,
	binding *store.WorktreeBinding,
) worktrees.Checkout {
	if s.Worktrees == nil || baseRepo.ID == "" || targetToplevel == "" {
		return worktrees.Checkout{}
	}
	targetCanon := canonicalGitPath(targetToplevel)
	if binding != nil && binding.RepoID == baseRepo.ID && binding.WorktreeID != "" {
		if c, err := s.Worktrees.Get(ctx, projectID, binding.WorktreeID); err == nil {
			return c
		}
	}
	if stored, err := s.Worktrees.ListStored(ctx, projectID, baseRepo.ID); err == nil {
		for _, item := range stored {
			if canonicalGitPath(item.WorktreePath) == targetCanon {
				return item
			}
		}
	}
	baseCanon := canonicalGitPath(baseRepo.Toplevel)
	if baseCanon == "" {
		baseCanon = targetCanon
	}
	commonDir, _, bare, ok := gitrepo.CommonRepository(baseCanon)
	if !ok || bare || commonDir == "" {
		return worktrees.Checkout{}
	}
	reg, ok := gitrepo.VerifyRegisteredCheckout(commonDir, targetCanon)
	if !ok {
		return worktrees.Checkout{}
	}
	c, _, err := s.Worktrees.EnsureRegisteredCheckout(ctx, worktrees.RepoSpec{
		ProjectID: projectID,
		RepoID:    baseRepo.ID,
		Toplevel:  baseCanon,
	}, reg)
	if err != nil {
		return worktrees.Checkout{}
	}
	return c
}

func buildLockedMutationContext(
	s *Handler,
	ctx context.Context,
	target CheckoutTarget,
	expectedHead *string,
	expectedRevision uint64,
) context.Context {
	return git.WithLockedVerify(ctx, func(c context.Context) error {
		if target.Explicit || (target.Checkout.ID != "" && s.Worktrees != nil) {
			if _, err := s.Worktrees.VerifyLive(c, target.BaseProject.ID, target.Checkout.ID); err != nil {
				return err
			}
		}
		if expectedHead != nil {
			head, _ := s.git.HeadSHA(c, target.Repo.Toplevel)
			if head != *expectedHead {
				return git.ErrExpectedHeadMismatch
			}
		}
		if expectedRevision != 0 {
			cached, ok := s.Board.StatusCache.Peek(target.Repo.Toplevel)
			if !ok || cached.Revision != expectedRevision {
				return git.ErrExpectedHeadMismatch
			}
		}
		return nil
	})
}

func writeCheckoutMutationError(
	s *Handler,
	w http.ResponseWriter,
	r *http.Request,
	fallbackCode wire.ApiErrorCode,
	fallbackMessage string,
	err error,
) {
	switch {
	case errors.Is(err, worktrees.ErrCheckoutNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeNotFound, WorktreeNotFoundMsg)
	case errors.Is(err, worktrees.ErrCheckoutRetired), errors.Is(err, worktrees.ErrCheckoutUnavailable):
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeUnavailable, WorktreeUnavailableMsg)
	case errors.Is(err, git.ErrExpectedHeadMismatch):
		s.responses.Fail(w, wire.ApiErrorCodeGitStateChanged, GitStateChangedMsg)
	default:
		s.gitCommandFailed(w, r, fallbackCode, fallbackMessage, err)
	}
}
