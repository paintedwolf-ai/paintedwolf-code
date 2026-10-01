package sourceapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/fileops"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/people"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func SourceOperationDTO(r fileops.Request) wire.SourceOperationStatus {
	var target struct {
		RootID          string `json:"root_id"`
		Path            string `json:"path"`
		From            string `json:"from"`
		To              string `json:"to"`
		ExpectedEntryID string `json:"expected_entry_id"`
	}
	if r.Method == http.MethodDelete {
		if uri, err := url.ParseRequestURI(r.URI); err == nil {
			target.RootID = uri.Query().Get("root_id")
			target.Path = uri.Query().Get("path")
		}
	} else {
		_ = json.Unmarshal(r.Body, &target)
	}
	if target.To != "" {
		target.Path = target.To
	}
	status := wire.SourceOperationStatus{
		ExpectedEntryID:  target.ExpectedEntryID,
		RootID:           target.RootID,
		Path:             target.Path,
		FromPath:         target.From,
		OperationID:      r.ID,
		Complete:         r.Terminal(),
		State:            r.State,
		Phase:            r.Phase,
		Operation:        r.Operation,
		Cancelable:       r.Cancelable,
		EntriesProcessed: r.EntriesProcessed,
		BytesProcessed:   r.BytesProcessed,
		CreatedAt:        r.CreatedAt,
		CompletedAt:      r.CompletedAt,
	}
	if r.Terminal() {
		if r.ResponseStatus >= 400 {
			var errResp wire.ErrorResponse
			if err := json.Unmarshal([]byte(r.ResponseBody), &errResp); err == nil {
				status.Error = &errResp
			}
		} else if len(r.ResponseBody) > 0 {
			var res any
			if err := json.Unmarshal([]byte(r.ResponseBody), &res); err == nil {
				status.Result = res
			}
		}
	}
	return status
}

func (s *Handler) sourceRequestForCaller(w http.ResponseWriter, r *http.Request) (fileops.Request, bool) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return fileops.Request{}, false
	}
	id := chi.URLParam(r, "operation_id")
	job, err := s.FileOperations.Get(r.Context(), id)
	caller := requestscope.Caller(r)
	if err != nil || job.PersonID != caller.ID || job.ProjectID != p.ID || !people.MayInvoke(caller, job.Operation) {
		if err != nil && !errors.Is(err, fileops.ErrNotFound) {
			s.responses.InternalError(w, r, err)
			return job, false
		}
		s.responses.FailReason(w, wire.ApiErrorCodeFileOperationNotFound, "file operation status is unavailable; review file history before retrying")
		return job, false
	}
	return job, true
}

func (s *Handler) HandleGetSourceOperation(w http.ResponseWriter, r *http.Request) {
	job, ok := s.sourceRequestForCaller(w, r)
	if ok {
		httpio.WriteJSON(w, http.StatusOK, SourceOperationDTO(job))
	}
}

var (
	sourceOperationPage   = httpio.MustPageLimit(50, 1, 100)
	sourceOperationCursor = pagecursor.For[sourceOperationPosition]("source_operations")
)

// sourceOperationPosition resumes after the last operation served.
type sourceOperationPosition struct {
	After string `json:"after"`
}

func (s *Handler) HandleListSourceOperations(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	query, err := httpio.ReadPageQuery(r, sourceOperationPage)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	caller := requestscope.Caller(r)
	scope := pagecursor.Scope(p.ID, caller.ID)
	jobs, err := s.FileOperations.List(r.Context(), p.ID, caller.ID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	var visible []fileops.Request
	for _, job := range jobs {
		if people.MayInvoke(caller, job.Operation) {
			visible = append(visible, job)
		}
	}
	start := 0
	if query.Cursor != "" {
		position, err := sourceOperationCursor.Decode(query.Cursor, scope)
		if err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		start = slices.IndexFunc(visible, func(job fileops.Request) bool { return job.ID == position.After }) + 1
		if start == 0 {
			// The operation the page ended on left the listed window.
			s.responses.PageCursorError(w, r, "cursor", pagecursor.ErrExpired)
			return
		}
	}
	end := min(start+query.Limit, len(visible))
	result := wire.SourceOperationList{Operations: make([]wire.SourceOperationStatus, 0, end-start)}
	for _, job := range visible[start:end] {
		result.Operations = append(result.Operations, SourceOperationDTO(job))
	}
	if end < len(visible) {
		next, err := sourceOperationCursor.Encode(scope, sourceOperationPosition{After: visible[end-1].ID})
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		result.NextCursor = next
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}

func (s *Handler) HandleCancelSourceOperation(w http.ResponseWriter, r *http.Request) {
	job, ok := s.sourceRequestForCaller(w, r)
	if !ok {
		return
	}
	if err := s.FileOperations.Cancel(job.ID); err != nil {
		s.writeSourceRequestError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) HandleRetrySourceOperation(w http.ResponseWriter, r *http.Request) {
	job, ok := s.sourceRequestForCaller(w, r)
	if !ok {
		return
	}
	operation, handler, ok := s.sourceRequestOperation(job.Operation)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeFileOperationUnavailable, "file operation is unavailable")
		return
	}
	original, _ := cloneSourceRequest(r, job.Body)
	parsed, err := url.ParseRequestURI(job.URI)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	original.Method, original.URL = job.Method, parsed
	original.ContentLength = int64(len(job.Body))
	if len(job.Body) > 0 {
		original.Header.Set("Content-Type", "application/json")
	}
	s.startSourceRequest(w, original, operation, handler, true)
}
