package requestscope

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// SessionProject resolves roots for session-scoped filesystem and Git access.
type SessionProject struct {
	Project *project.Project
	Roots   []projectroot.RootRef
	Binding *store.WorktreeBinding
}

func ResolveSessionProject(storeReader session.Store, ctx context.Context, p *project.Project, sessionID string) (SessionProject, error) {
	roots := project.RootRefsFrom(p)
	scope := SessionProject{Project: p, Roots: roots}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return scope, nil
	}
	sess, err := storeReader.Get(ctx, sessionID)
	if err != nil {
		return SessionProject{}, err
	}
	if sess == nil || strings.TrimSpace(sess.ProjectID) != strings.TrimSpace(p.ID) {
		return SessionProject{}, store.ErrSessionNotFound
	}
	row, bound, err := storeReader.GetWorktreeBinding(ctx, sessionID)
	if err != nil {
		return SessionProject{}, err
	}
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
		return SessionProject{}, session.ErrSessionWorktreeStale
	}
	roots = project.SubstituteWorktreeRoots(roots, binding)
	scope.Project = project.WithWorktree(p, binding)
	scope.Roots = roots
	scope.Binding = row
	return scope, nil
}

func ProjectForRequest(storeReader session.Store, responses *httpio.Responder, w http.ResponseWriter, r *http.Request, p *project.Project) (*project.Project, bool) {
	scope, ok := SessionProjectForRequest(storeReader, responses, w, r, p)
	if !ok {
		return nil, false
	}
	return scope.Project, true
}

// SessionProjectForRequest keeps the worktree binding that resolved the
// project; a binding means the chat's own checkout answered, not the project's.
func SessionProjectForRequest(storeReader session.Store, responses *httpio.Responder, w http.ResponseWriter, r *http.Request, p *project.Project) (SessionProject, bool) {
	scope, err := ResolveSessionProject(storeReader, r.Context(), p, r.URL.Query().Get("session_id"))
	if err != nil {
		writeSessionProjectError(responses, w, r, err)
		return SessionProject{}, false
	}
	return scope, true
}

func ProjectForSession(storeReader session.Store, responses *httpio.Responder, w http.ResponseWriter, r *http.Request, p *project.Project, sessionID string) (*project.Project, bool) {
	scope, err := ResolveSessionProject(storeReader, r.Context(), p, sessionID)
	if err != nil {
		writeSessionProjectError(responses, w, r, err)
		return nil, false
	}
	return scope.Project, true
}

func writeSessionProjectError(responses *httpio.Responder, w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, session.ErrSessionWorktreeStale):
		responses.Fail(w, wire.ApiErrorCodeWorktreeStale, "This chat's worktree is missing or invalid. Unbind it in the Git tab to continue.")
	case errors.Is(err, store.ErrSessionNotFound):
		responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "Session not found.")
	default:
		responses.InternalError(w, r, err)
	}
}

// Session writes 404 for an absent session and 500 for a store failure.
// The boolean reports whether the handler may continue.
func Session(storeReader session.Store, responses *httpio.Responder, w http.ResponseWriter, r *http.Request, id string) (*wire.Session, bool) {
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
func SessionExists(storeReader session.Store, responses *httpio.Responder, w http.ResponseWriter, r *http.Request, id string) bool {
	_, ok := Session(storeReader, responses, w, r, id)
	return ok
}
