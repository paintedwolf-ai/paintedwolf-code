package sourceapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/project"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// sourceHandleError is a view, presentation, or interest path parameter that
// is not a UUID.
type sourceHandleError struct{ param string }

func (e *sourceHandleError) Error() string { return e.param + " must be a UUID" }

// errSourceViewEditGone is a chat file edit a retained view compares that the
// chat no longer records.
var errSourceViewEditGone = errors.New("source view file edit not found")

// sourceHandleParam reads a view, presentation, or interest handle from the path.
func sourceHandleParam(r *http.Request, name string) (string, error) {
	id := chi.URLParam(r, name)
	if _, err := uuid.Parse(id); err != nil {
		return "", &sourceHandleError{param: name}
	}
	return id, nil
}

func (s *Views) requestedSourceView(w http.ResponseWriter, r *http.Request) (*sourceView, *http.Request, func(), bool) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return nil, r, nil, false
	}
	id, err := sourceHandleParam(r, "view_id")
	if err != nil {
		s.writeSourceViewAccessError(w, r, err)
		return nil, r, nil, false
	}
	scope := pagedview.Scope{Person: requestscope.Caller(r).ID, Project: p.ID}
	view, release, err := s.sourceViewRegistry().registry.Acquire(scope, id)
	if err != nil {
		s.writeSourceViewAccessError(w, r, err)
		return nil, r, nil, false
	}
	scoped, err := s.sourceViewProject(r.Context(), p, view)
	if err != nil {
		release()
		s.writeSourceViewAccessError(w, r, err)
		return nil, r, nil, false
	}
	view.mu.Lock()
	current := view.comparisonData.current
	view.mu.Unlock()
	if current != nil {
		scopeErr := validateSourceTreeAddress(scoped, wire.SourceTreeAddress{RootID: current.stream.RootID, Path: current.stream.Path})
		if scopeErr != nil {
			release()
			s.writeSourceViewAccessError(w, r, scopeErr)
			return nil, r, nil, false
		}
	}
	s.ComparisonViews.refreshComparisonScreen(view)
	view.touch()
	s.sourceViewRegistry().presentations.Renew(func(p *sourcePresentation) bool { return p.read.id == view.id })
	ctx, cancel := context.WithCancelCause(r.Context())
	stop := context.AfterFunc(view.ctx, func() { cancel(pagedview.ErrExpired) }) //nolint:contextcheck // View expiry also cancels the request.
	return view, r.WithContext(ctx), func() { stop(); cancel(context.Canceled); release() }, true
}

// Every retained read rechecks the current project and session relationship.
func (s *Views) sourceViewProject(ctx context.Context, p *project.Project, view *sourceView) (*project.Project, error) {
	scope, err := requestscope.ResolveSessionProject(s.SessionStore, ctx, p, view.sessionID)
	if err != nil {
		return nil, err
	}
	if scope.Project.WorkspaceID() != view.workspaceID {
		view.cancel()
		s.sourceViewRegistry().presentations.ReleaseWhere(func(p *sourcePresentation) bool { return p.read.id == view.id })
		s.sourceViewRegistry().registry.Release(pagedview.Scope{Person: view.scope.Person, Project: p.ID}, view.id)
		return nil, pagedview.ErrExpired
	}
	view.mu.Lock()
	source := view.comparisonData.chatSource
	if view.comparisonData.comparisonSource.Chat != nil {
		source = view.comparisonData.comparisonSource.Chat
	}
	view.mu.Unlock()
	if source != nil {
		if _, err := s.Comparisons.readChatFileEdit(ctx, view.sessionID, source.First); err != nil {
			return nil, errSourceViewEditGone
		}
		if _, err := s.Comparisons.readChatFileEdit(ctx, view.sessionID, source.Last); err != nil {
			return nil, errSourceViewEditGone
		}
	}
	return scope.Project, nil
}

func (s *Views) HandleGetSourceView(w http.ResponseWriter, r *http.Request) {
	view, r, release, ok := s.requestedSourceView(w, r)
	if !ok {
		return
	}
	defer release()
	state, err := view.snapshot(r.Context()) //nolint:contextcheck // requestedSourceView derives this request context from the incoming request.
	if err != nil {
		s.writeSourceViewError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, state)
}

// HandleReleaseSourceView: the caller's person and project scope authorizes
// release, so a view whose chat was deleted can still be released. A view the
// host does not retain answers source_view_not_found.
func (s *Views) HandleReleaseSourceView(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	id, err := sourceHandleParam(r, "view_id")
	if err != nil {
		s.writeSourceViewAccessError(w, r, err)
		return
	}
	scope := pagedview.Scope{Person: requestscope.Caller(r).ID, Project: p.ID}
	registry := s.sourceViewRegistry().registry
	view, release, err := registry.Acquire(scope, id)
	if err != nil {
		s.writeSourceViewAccessError(w, r, err)
		return
	}
	defer release()
	view.cancel()
	s.sourceViewRegistry().presentations.ReleaseWhere(func(p *sourcePresentation) bool { return p.read.id == view.id })
	registry.Release(scope, id)
	w.WriteHeader(http.StatusNoContent)
}

// writeSourceViewAccessError answers the errors of addressing a retained view
// and rechecking the chat and folders it reads.
func (s *Views) writeSourceViewAccessError(w http.ResponseWriter, r *http.Request, err error) {
	if !s.writeSourceViewAddressError(w, r, err) {
		s.responses.InternalError(w, r, err)
	}
}

func (s *Views) writeSourceViewAddressError(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, context.Canceled) && errors.Is(context.Cause(r.Context()), pagedview.ErrExpired) {
		err = pagedview.ErrExpired
	}
	var handle *sourceHandleError
	switch {
	case errors.As(err, &handle):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, handle.Error())
	case errors.Is(err, sessionscope.ErrWorktreeStale):
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeStale, "This chat's worktree is missing or invalid. Unbind it in the Git tab to continue.")
	case errors.Is(err, store.ErrSessionNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "This chat no longer exists.")
	case errors.Is(err, sourcetree.ErrUnknownRoot):
		s.responses.Fail(w, wire.ApiErrorCodeRootNotFound, "That folder is not attached to this project.")
	case errors.Is(err, errSourceViewEditGone):
		s.responses.Fail(w, wire.ApiErrorCodeSourceEffectNotFound, "File edit not found.")
	case errors.Is(err, pagedview.ErrExpired):
		slog.InfoContext(r.Context(), "Source view request named a view the host does not retain", "method", r.Method, "path", r.URL.Path, "error", err)
		s.responses.Fail(w, wire.ApiErrorCodeSourceViewNotFound, "This source view is no longer open. Reopen it to continue.")
	default:
		return false
	}
	return true
}

func (s *Views) writeSourceViewError(w http.ResponseWriter, r *http.Request, err error) {
	if s.writeSourceViewAddressError(w, r, err) {
		return
	}
	switch {
	case errors.Is(err, pagecursor.ErrInvalid):
		s.responses.Fail(w, wire.ApiErrorCodeInvalidPageCursor, "The source view cursor is invalid for this query.")
	case errors.Is(err, pagedview.ErrPreparing):
		s.responses.Fail(w, wire.ApiErrorCodeSourceViewPreparing, "The source presentation is being prepared.")
	case errors.Is(err, pagedview.ErrRevision):
		s.responses.Fail(w, wire.ApiErrorCodeSourceViewRevisionChanged, "The source view changed. Refresh its position and try again.")
	case errors.Is(err, pagedview.ErrOperationConflict):
		s.responses.Fail(w, wire.ApiErrorCodeIdempotencyConflict, "This operation ID was already used for a different source view intent.")
	case pagedview.StorageFull(err):
		s.responses.Fail(w, wire.ApiErrorCodeSourceViewCapacity, sourceStorageFullMessage)
	case errors.Is(err, pagedview.ErrBudget):
		s.responses.Fail(w, wire.ApiErrorCodeSourceViewCapacity, sourceViewCapacityMessage)
	case errors.Is(err, pagedview.ErrRange), errors.Is(err, sourcetree.ErrAddress):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "The source view address or range is invalid.")
	case errors.Is(err, pagedview.ErrFrameSize):
		s.responses.Fail(w, wire.ApiErrorCodeSourceViewFrameTooLarge, "The requested source row exceeds the presentation limit.")
	default:
		s.Comparisons.writeComparisonError(w, r, err)
	}
}
