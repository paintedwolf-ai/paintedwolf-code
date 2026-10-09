package sourceapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Mutations) beginProjectSourceMutation(
	w http.ResponseWriter,
	r *http.Request,
) (*project.Project, func(), bool) {
	if held, ok := r.Context().Value(sourceRequestProjectKey{}).(*project.Project); ok && held.ID == strings.TrimSpace(chi.URLParam(r, "id")) {
		return held, func() {}, true
	}
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return nil, nil, false
	}
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		release()
		return nil, nil, false
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		release()
		return nil, nil, false
	}
	return p, release, true
}

func (s *Mutations) HandleReplaceProjectSource(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	var req wire.PutProjectSourceRequest
	if err := httpio.DecodeSourceJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	if strings.TrimSpace(req.Path) == "" || strings.TrimSpace(req.BaseSHA256) == "" || req.Encoding == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "path, encoding, and base_sha256 are required")
		return
	}
	sessionID, turn := s.Workspace.UserSourceChatAffiliation(r)
	result, err := s.SourceMutations.Write(r.Context(), operationID.String(), p, projectsource.SourceWriteRequest{
		Path:       req.Path,
		RootID:     strings.TrimSpace(req.RootID),
		Content:    req.Content,
		Encoding:   string(req.Encoding),
		BaseSHA256: req.BaseSHA256,
		SessionID:  sessionID,
		Turn:       turn,
	})
	if errors.Is(err, projectsource.ErrSourceBinary) {
		s.responses.Fail(w, wire.ApiErrorCodeSourceBinaryDenied, "binary content cannot be written as text")
		return
	}
	if err != nil {
		s.Workspace.WriteProjectSourceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ProjectSourceWriteResponse{
		Path:      result.Path,
		SizeBytes: result.SizeBytes,
		SHA256:    result.SHA256,
	})
}

func (s *Mutations) HandleMakeProjectSourceEditable(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	var req wire.MakeProjectSourceEditableRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Path) == "" || strings.TrimSpace(req.RootID) == "" || strings.TrimSpace(req.BaseSHA256) == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "path, root_id, and base_sha256 are required")
		return
	}
	result, err := projectsource.MakeSourceEditable(p, projectsource.SourceMakeEditableRequest{
		Path: req.Path, RootID: req.RootID, BaseSHA256: req.BaseSHA256,
	})
	if err != nil {
		s.Workspace.WriteProjectSourceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.MakeProjectSourceEditableResponse{
		Path: result.Path, RootID: result.RootID,
		PreviousMode: result.PreviousMode, Mode: result.Mode, Writable: result.Writable,
	})
}

func (s *Mutations) HandleCreateProjectSourceEntry(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	var req wire.CreateProjectSourceEntryRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "path is required")
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	sessionID, turn := s.Workspace.UserSourceChatAffiliation(r)
	rel, err := s.SourceMutations.Create(r.Context(), operationID.String(), p, projectsource.SourceEntryCreateRequest{
		Path:      req.Path,
		Kind:      projectsource.SourceEntryKind(strings.TrimSpace(req.Kind)),
		RootID:    strings.TrimSpace(req.RootID),
		SessionID: sessionID,
		Turn:      turn,
	})
	if err != nil {
		s.Workspace.WriteProjectSourceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, wire.ProjectSourceEntryCreatedResponse{Path: rel})
}

func (s *Mutations) HandleRenameProjectSource(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	var req wire.RenameProjectSourceRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.From) == "" || strings.TrimSpace(req.To) == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "from and to are required")
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	sessionID, turn := s.Workspace.UserSourceChatAffiliation(r)
	var retargetID string
	result, err := s.SourceMutations.Rename(r.Context(), operationID.String(), p, projectsource.SourceRenameRequest{
		RootID: strings.TrimSpace(req.RootID),
		From:   req.From,
		To:     req.To,
		Prepare: func(ctx context.Context, plan projectsource.SourceRenamePlan) error {
			var prepareErr error
			retargetID, prepareErr = s.EditorDocuments.PrepareRetarget(ctx, p, plan)
			return prepareErr
		},
		SessionID: sessionID,
		Turn:      turn,
	})
	var retargetErr error
	if retargetID != "" {
		retargetErr = s.EditorDocuments.ReconcileRetarget(r.Context(), retargetID)
	}
	if err == nil {
		retargetErr = errors.Join(retargetErr, s.EditorDocuments.ReconcilePendingRetargets(r.Context()))
	}
	if err != nil {
		if retargetErr != nil {
			s.responses.Logger.ErrorContext(r.Context(), "reconcile editor documents after source rename failure", "error", retargetErr)
		}
		s.Workspace.WriteProjectSourceError(w, r, err)
		return
	}
	if retargetErr != nil {
		s.responses.InternalError(w, r, retargetErr)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ProjectSourceLifecycleResponse{
		RootID: result.RootID,
		Path:   result.Path,
	})
}

func (s *Mutations) HandleCopyProjectSource(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	var req wire.CopyProjectSourceRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.From) == "" || strings.TrimSpace(req.To) == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "from and to are required")
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	sessionID, turn := s.Workspace.UserSourceChatAffiliation(r)
	result, err := s.SourceMutations.Copy(r.Context(), operationID.String(), p, projectsource.SourceCopyRequest{
		RootID:    strings.TrimSpace(req.RootID),
		From:      req.From,
		To:        req.To,
		SessionID: sessionID,
		Turn:      turn,
	})
	if err != nil {
		s.Workspace.WriteProjectSourceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ProjectSourceLifecycleResponse{
		RootID: result.RootID,
		Path:   result.Path,
	})
}

func (s *Mutations) HandleDeleteProjectSource(w http.ResponseWriter, r *http.Request) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	q := r.URL.Query()
	pathQuery := q.Get("path")
	if pathQuery == "" {
		s.responses.InvalidQueryParam(w, "path", "is required")
		return
	}
	recursive, _, queryErr := httpio.OptionalBoolQuery(r, "recursive")
	if queryErr != nil {
		s.responses.InvalidQuery(w, queryErr)
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(q.Get("operation_id")))
	if err != nil {
		s.responses.InvalidQueryParam(w, "operation_id", "must be a UUID")
		return
	}
	sessionID, turn := s.Workspace.UserSourceChatAffiliation(r)
	err = s.SourceMutations.Delete(r.Context(), operationID.String(), p, projectsource.SourceDeleteRequest{
		RootID:    strings.TrimSpace(q.Get("root_id")),
		Path:      pathQuery,
		Recursive: recursive,
		SessionID: sessionID,
		Turn:      turn,
	})
	if err != nil {
		s.Workspace.WriteProjectSourceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sourceIndexSnapshot returns the project id used by the poll clock.
