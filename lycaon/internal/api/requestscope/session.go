package requestscope

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	"github.com/lycaon/lycaon/internal/projectroot"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var (
	ErrInvalidWorktreeID      = errors.New("worktree_id must be a valid UUID")
	ErrConflictingWorktreeIDs = errors.New("multiple worktree_id values address the same repository")
)

type Sessions interface {
	Get(context.Context, string) (*wire.Session, error)
	GetWorktreeBinding(context.Context, string) (*store.WorktreeBinding, bool, error)
	ResolveEffectiveMode(context.Context, string) (store.RootAuthority, error)
}

// SessionProject resolves roots for session-scoped and checkout-scoped filesystem and Git access.
type SessionProject struct {
	Project   *project.Project
	Roots     []projectroot.RootRef
	Binding   *store.WorktreeBinding
	Checkouts []worktrees.Checkout
}

func checkoutPathAdmitted(worktreePath string) bool {
	if strings.TrimSpace(worktreePath) == "" {
		return false
	}
	if refused, _ := confine.AttachedWriteRootRefused(worktreePath); refused {
		return false
	}
	return !confine.ControlPlaneWriteDenied(worktreePath)
}

func cleanWorktreeIDs(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		id := strings.TrimSpace(entry)
		if id == "" {
			return nil, ErrInvalidWorktreeID
		}
		if _, err := uuid.Parse(id); err != nil {
			return nil, ErrInvalidWorktreeID
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func ResolveSessionProject(storeReader Sessions, ctx context.Context, p *project.Project, sessionID string) (SessionProject, error) {
	return ResolveSessionProjectWithWorktrees(storeReader, ctx, p, sessionID, nil)
}

// ResolveSessionProjectWithWorktrees resolves an optional session binding and explicit checkout overrides.
func ResolveSessionProjectWithWorktrees(storeReader Sessions, ctx context.Context, p *project.Project, sessionID string, rawWorktreeIDs []string) (SessionProject, error) {
	worktreeIDs, err := cleanWorktreeIDs(rawWorktreeIDs)
	if err != nil {
		return SessionProject{}, err
	}
	roots := project.RootRefsFrom(p)
	scope := SessionProject{Project: p, Roots: roots}
	sessionID = strings.TrimSpace(sessionID)

	var row *store.WorktreeBinding
	var bound bool
	if sessionID != "" {
		sess, getErr := storeReader.Get(ctx, sessionID)
		if getErr != nil {
			return SessionProject{}, getErr
		}
		if sess == nil || strings.TrimSpace(sess.ProjectID) != strings.TrimSpace(p.ID) {
			return SessionProject{}, store.ErrSessionNotFound
		}
		row, bound, err = storeReader.GetWorktreeBinding(ctx, sessionID)
		if err != nil {
			return SessionProject{}, err
		}
	}

	if len(worktreeIDs) == 0 {
		if !bound || row == nil {
			return scope, nil
		}
		binding := project.Binding{
			ID:           row.WorktreeID,
			Toplevel:     row.Toplevel,
			WorktreePath: row.WorktreePath,
			Branch:       row.Branch,
			BaseBranch:   row.BaseBranch,
		}
		if err := git.NewManager().ValidateWorktree(ctx, binding.Toplevel, binding.WorktreePath, binding.Branch); err != nil {
			return SessionProject{}, sessionscope.ErrWorktreeStale
		}
		roots = project.SubstituteWorktreeRoots(roots, binding)
		scope.Project = project.WithWorktree(p, binding)
		scope.Roots = roots
		scope.Binding = row
		return scope, nil
	}

	dbProvider, ok := storeReader.(interface{ DB() db.Handle })
	if !ok || dbProvider.DB() == nil {
		return SessionProject{}, worktrees.ErrCheckoutNotFound
	}
	database := dbProvider.DB()

	seenRepos := make(map[string]bool, len(worktreeIDs))
	seenToplevels := make(map[string]bool, len(worktreeIDs))
	checkouts := make([]worktrees.Checkout, 0, len(worktreeIDs))
	for _, id := range worktreeIDs {
		checkout, verifyErr := worktrees.VerifyLiveCheckout(ctx, database, p.ID, id)
		if verifyErr != nil {
			return SessionProject{}, verifyErr
		}
		if !checkoutPathAdmitted(checkout.WorktreePath) {
			return SessionProject{}, worktrees.ErrCheckoutUnavailable
		}
		topKey := gitrepo.CanonicalDir(checkout.Toplevel)
		if (checkout.RepoID != "" && seenRepos[checkout.RepoID]) || (topKey != "" && seenToplevels[topKey]) {
			return SessionProject{}, ErrConflictingWorktreeIDs
		}
		if checkout.RepoID != "" {
			seenRepos[checkout.RepoID] = true
		}
		if topKey != "" {
			seenToplevels[topKey] = true
		}
		checkouts = append(checkouts, checkout)
	}

	curProject := p
	if bound && row != nil {
		rowTop := gitrepo.CanonicalDir(row.Toplevel)
		overridden := (row.RepoID != "" && seenRepos[row.RepoID]) || (rowTop != "" && seenToplevels[rowTop])
		if !overridden {
			binding := project.Binding{
				ID:           row.WorktreeID,
				Toplevel:     row.Toplevel,
				WorktreePath: row.WorktreePath,
				Branch:       row.Branch,
				BaseBranch:   row.BaseBranch,
			}
			if err := git.NewManager().ValidateWorktree(ctx, binding.Toplevel, binding.WorktreePath, binding.Branch); err != nil {
				return SessionProject{}, sessionscope.ErrWorktreeStale
			}
			roots = project.SubstituteWorktreeRoots(roots, binding)
			curProject = project.WithWorktree(curProject, binding)
			scope.Binding = row
		} else {
			for _, checkout := range checkouts {
				if row.WorktreeID != "" && checkout.ID == row.WorktreeID {
					scope.Binding = row
					break
				}
			}
		}
	}

	for _, checkout := range checkouts {
		binding := project.Binding{
			ID:           checkout.ID,
			Toplevel:     checkout.Toplevel,
			WorktreePath: checkout.WorktreePath,
			Branch:       checkout.Branch,
			BaseBranch:   checkout.BaseBranch,
		}
		roots = project.SubstituteWorktreeRoots(roots, binding)
		curProject = project.WithWorktree(curProject, binding)
	}

	scope.Project = curProject
	scope.Roots = roots
	scope.Checkouts = checkouts
	return scope, nil
}

func ProjectForRequest(storeReader Sessions, responses *httpio.Responder, w http.ResponseWriter, r *http.Request, p *project.Project) (*project.Project, bool) {
	scope, ok := SessionProjectForRequest(storeReader, responses, w, r, p)
	if !ok {
		return nil, false
	}
	return scope.Project, true
}

// SessionProjectForRequest keeps the worktree binding or explicit checkouts that resolved the project.
func SessionProjectForRequest(storeReader Sessions, responses *httpio.Responder, w http.ResponseWriter, r *http.Request, p *project.Project) (SessionProject, bool) {
	var rawWorktreeIDs []string
	if r.URL != nil {
		rawWorktreeIDs = r.URL.Query()["worktree_id"]
	}
	scope, err := ResolveSessionProjectWithWorktrees(storeReader, r.Context(), p, r.URL.Query().Get("session_id"), rawWorktreeIDs)
	if err != nil {
		writeSessionProjectError(responses, w, r, err)
		return SessionProject{}, false
	}
	return scope, true
}

func ProjectForSession(storeReader Sessions, responses *httpio.Responder, w http.ResponseWriter, r *http.Request, p *project.Project, sessionID string) (*project.Project, bool) {
	scope, err := ResolveSessionProject(storeReader, r.Context(), p, sessionID)
	if err != nil {
		writeSessionProjectError(responses, w, r, err)
		return nil, false
	}
	return scope.Project, true
}

func writeSessionProjectError(responses *httpio.Responder, w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalidWorktreeID):
		responses.InvalidQueryParam(w, "worktree_id", "must be a valid checkout UUID")
	case errors.Is(err, ErrConflictingWorktreeIDs):
		responses.InvalidQueryParam(w, "worktree_id", "cannot select multiple checkouts for the same repository")
	case errors.Is(err, worktrees.ErrCheckoutNotFound):
		responses.Fail(w, wire.ApiErrorCodeWorktreeNotFound, "That checkout is not registered for this repository.")
	case errors.Is(err, worktrees.ErrCheckoutRetired), errors.Is(err, worktrees.ErrCheckoutUnavailable):
		responses.Fail(w, wire.ApiErrorCodeWorktreeUnavailable, "That checkout is no longer available on disk.")
	case errors.Is(err, sessionscope.ErrWorktreeStale):
		responses.Fail(w, wire.ApiErrorCodeWorktreeStale, "This chat's worktree is missing or invalid. Unbind it in the Git tab to continue.")
	case errors.Is(err, store.ErrSessionNotFound):
		responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "Session not found.")
	default:
		responses.InternalError(w, r, err)
	}
}

// Session writes 404 for an absent session and 500 for a store failure.
// The boolean reports whether the handler may continue.
func Session(storeReader Sessions, responses *httpio.Responder, w http.ResponseWriter, r *http.Request, id string) (*wire.Session, bool) {
	sess, err := storeReader.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "session not found")
			return nil, false
		}
		responses.InternalError(w, r, err)
		return nil, false
	}
	return sess, true
}

// SessionExists checks membership and writes the corresponding lookup error.
func SessionExists(storeReader Sessions, responses *httpio.Responder, w http.ResponseWriter, r *http.Request, id string) bool {
	_, ok := Session(storeReader, responses, w, r, id)
	return ok
}
