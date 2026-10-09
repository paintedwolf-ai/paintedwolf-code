package sourceapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/fileops"
	"github.com/lycaon/lycaon/internal/projectsource"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// publishSourceRequest sends each persisted file operation state as a source_operation event.
func publishSourceRequest(hub events.ReplayHub, request fileops.Request) {
	if hub == nil {
		return
	}
	_ = hub.Publish(context.Background(), wire.EventTopicSourceOperation,
		events.PublishKey{Project: request.ProjectID},
		wire.SourceOperationEvent{ProjectID: request.ProjectID, Operation: SourceOperationDTO(request)})
}

type sourceRequestProjectKey struct{}

func (s *Mutations) SourceRequestHandler(operation Operation, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.startSourceRequest(w, r, operation, next, false) }
}

func (s *Mutations) startSourceRequest(w http.ResponseWriter, r *http.Request, operation Operation, next http.HandlerFunc, retry bool) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	body, id, ok := s.admitSourceRequestBody(w, r, operation)
	if !ok {
		release()
		return
	}
	digest := sha256.Sum256(append([]byte(r.Method+"\x00"+r.URL.Path+"?"+r.URL.Query().Encode()+"\x00"), body...))
	sessionID, turn := s.Workspace.UserSourceChatAffiliation(r)
	request := fileops.Request{SessionID: sessionID, Turn: turn, ID: id, ProjectID: chi.URLParam(r, "id"), PersonID: requestscope.Caller(r).ID,
		Operation: operation.ID, Method: r.Method, URI: r.URL.RequestURI(), Body: body, InputDigest: hex.EncodeToString(digest[:]), RootScope: sourceTreeCursorScope(p, "", "")}
	run, created, err := s.FileOperations.Admit(r.Context(), request, retry)
	if err != nil {
		release()
		s.writeSourceRequestError(w, r, err)
		return
	}
	if created {
		// The mutation lease protects the admitted root binding while the job is queued.
		owned, route := cloneSourceRequest(r, body)
		s.background.Go(r.Context(), func(ctx context.Context) {
			defer release()
			run.Execute(ctx, func(ctx context.Context) fileops.Outcome {
				ctx = projectsource.WithSourceProgress(ctx, func(progress projectsource.SourceProgress) {
					run.Report(ctx, progress.Phase, progress.Entries, progress.Bytes)
				})
				ctx = projectsource.WithSourceEffect(ctx, func() error { return run.BeginEffect(ctx) })
				ctx = context.WithValue(ctx, sourceRequestProjectKey{}, p)
				affiliation := run.Snapshot()
				ctx = context.WithValue(ctx, sourceRequestAffiliationKey{}, sourceRequestAffiliation{sessionID: affiliation.SessionID, turn: affiliation.Turn})
				response := &sourceResponse{header: make(http.Header)}
				next(response, owned.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route)))
				return fileops.Outcome{Status: response.status, Body: response.body.String()}
			})
		})
	} else {
		release()
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-run.Done():
		state := run.Snapshot()
		if retry || state.State == "interrupted" || state.State == "canceled" {
			httpio.WriteJSON(w, http.StatusAccepted, SourceOperationDTO(state))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(state.ResponseStatus)
		_, _ = io.WriteString(w, state.ResponseBody)
	case <-timer.C:
		httpio.WriteJSON(w, http.StatusAccepted, SourceOperationDTO(run.Snapshot()))
	case <-r.Context().Done():
	}
}

// sourceRequestBody is the request body a journaled operation carries, for
// strict decoding before admission; a bodyless delete carries none.
func (s *Mutations) sourceRequestBody(operationID string) (any, bool) {
	switch operationID {
	case s.operations.CreateProjectSourceEntry.ID:
		return new(wire.CreateProjectSourceEntryRequest), true
	case s.operations.RenameProjectSource.ID:
		return new(wire.RenameProjectSourceRequest), true
	case s.operations.CopyProjectSource.ID:
		return new(wire.CopyProjectSourceRequest), true
	case s.operations.UndoProjectSourceHistory.ID, s.operations.RedoProjectSourceHistory.ID:
		return new(wire.SourceHistoryMutationRequest), true
	default:
		return nil, false
	}
}

// admitSourceRequestBody reads and validates the request a file operation
// journals: its body strictly against the operation's schema, and its
// operation_id, from the body or, for a bodyless delete, the query.
func (s *Mutations) admitSourceRequestBody(w http.ResponseWriter, r *http.Request, operation Operation) ([]byte, string, bool) {
	dst, carriesBody := s.sourceRequestBody(operation.ID)
	if !carriesBody {
		id, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("operation_id")))
		if err != nil {
			s.responses.InvalidQueryParam(w, "operation_id", "must be a UUID")
			return nil, "", false
		}
		return nil, id.String(), true
	}
	if err := httpio.RequireRequestMediaType(r, httpio.MediaTypeJSON); err != nil {
		s.responses.DecodeError(w, r, err)
		return nil, "", false
	}
	body, err := httpio.ReadAllBody(w, r)
	if err != nil {
		s.responses.DecodeError(w, r, err)
		return nil, "", false
	}
	if err := httpio.DecodeStrictJSON(bytes.NewReader(body), dst); err != nil {
		s.responses.DecodeError(w, r, err)
		return nil, "", false
	}
	var keyed struct {
		OperationID string `json:"operation_id"`
	}
	_ = json.Unmarshal(body, &keyed)
	id, err := uuid.Parse(strings.TrimSpace(keyed.OperationID))
	if err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest,
			map[string]any{"field": "operation_id", "reason": "must be a UUID"}, "operation_id must be a UUID")
		return nil, "", false
	}
	return body, id.String(), true
}

func cloneSourceRequest(r *http.Request, body []byte) (*http.Request, *chi.Context) {
	owned := r.Clone(r.Context())
	route := chi.NewRouteContext()
	if current := chi.RouteContext(r.Context()); current != nil {
		route.URLParams.Keys = append([]string(nil), current.URLParams.Keys...)
		route.URLParams.Values = append([]string(nil), current.URLParams.Values...)
	}
	owned = owned.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
	owned.Body = io.NopCloser(bytes.NewReader(body))
	return owned, route
}

type sourceResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *sourceResponse) Header() http.Header { return w.header }
func (w *sourceResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *sourceResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

func (s *Mutations) sourceRequestOperation(id string) (Operation, http.HandlerFunc, bool) {
	switch id {
	case s.operations.DeleteProjectSource.ID:
		return s.operations.DeleteProjectSource, s.HandleDeleteProjectSource, true
	case s.operations.RenameProjectSource.ID:
		return s.operations.RenameProjectSource, s.HandleRenameProjectSource, true
	case s.operations.CopyProjectSource.ID:
		return s.operations.CopyProjectSource, s.HandleCopyProjectSource, true
	case s.operations.CreateProjectSourceEntry.ID:
		return s.operations.CreateProjectSourceEntry, s.HandleCreateProjectSourceEntry, true
	case s.operations.UndoProjectSourceHistory.ID:
		return s.operations.UndoProjectSourceHistory, s.HandleUndoProjectSourceHistory, true
	case s.operations.RedoProjectSourceHistory.ID:
		return s.operations.RedoProjectSourceHistory, s.HandleRedoProjectSourceHistory, true
	default:
		return Operation{}, nil, false
	}
}

func (s *Mutations) writeSourceRequestError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, fileops.ErrConflict):
		s.responses.Fail(w, wire.ApiErrorCodeIdempotencyConflict, "operation_id was already used for a different file request")
	case errors.Is(err, fileops.ErrCapacity):
		w.Header().Set("Retry-After", "1")
		s.responses.Fail(w, wire.ApiErrorCodeFileOperationCapacity, "file operation queue is full")
	case errors.Is(err, fileops.ErrNotCancelable):
		s.responses.Fail(w, wire.ApiErrorCodeFileOperationNotCancelable, "file operation has entered its commit phase")
	default:
		s.responses.InternalError(w, r, err)
	}
}

// RecoverFileOperations runs after filesystem and editor-document reconciliation.
func (s *Mutations) RecoverFileOperations(ctx context.Context) error {
	return s.FileOperations.Recover(ctx, func(ctx context.Context, job fileops.Request) (fileops.Outcome, bool, error) {
		raw, committed, err := s.SourceMutations.Journal.CommittedSourceResult(ctx, job.ID)
		if err != nil || !committed {
			return fileops.Outcome{}, false, err
		}
		var result any
		status := http.StatusOK
		switch job.Operation {
		case s.operations.DeleteProjectSource.ID:
			return fileops.Outcome{Status: http.StatusNoContent}, true, nil
		case s.operations.CreateProjectSourceEntry.ID:
			var value struct{ Path string }
			if err := json.Unmarshal(raw, &value); err != nil {
				return fileops.Outcome{}, false, err
			}
			result = wire.ProjectSourceEntryCreatedResponse{Path: value.Path}
			status = http.StatusCreated
		case s.operations.CopyProjectSource.ID, s.operations.RenameProjectSource.ID:
			var value projectsource.SourceLifecycleResult
			if err := json.Unmarshal(raw, &value); err != nil {
				return fileops.Outcome{}, false, err
			}
			result = wire.ProjectSourceLifecycleResponse{RootID: value.RootID, Path: value.Path}
		case s.operations.UndoProjectSourceHistory.ID, s.operations.RedoProjectSourceHistory.ID:
			var value projectsource.SourceHistoryMutationResult
			if err := json.Unmarshal(raw, &value); err != nil {
				return fileops.Outcome{}, false, err
			}
			result = wire.SourceHistoryMutationResponse{EntryID: value.EntryID, RootID: value.RootID, Path: value.Path, FromPath: value.FromPath, Op: wire.SourceChangeOp(value.Op), IsDir: value.IsDir}
		default:
			return fileops.Outcome{}, false, nil
		}
		encoded, err := json.Marshal(result)
		return fileops.Outcome{Status: status, Body: string(encoded)}, err == nil, err
	})
}

type sourceRequestAffiliationKey struct{}
type sourceRequestAffiliation struct {
	sessionID string
	turn      int
}
