package sessionadmin

import (
	"errors"
	"net/http"
	"regexp"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var (
	receiptDocumentLimit = httpio.MustPageLimit(64, 1, 500)
	receiptSearchLimit   = httpio.MustPageLimit(50, 1, 500)
	receiptRevision      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (s *Transcript) HandleGetCommandReceiptDocument(w http.ResponseWriter, r *http.Request) {
	reader, scopes, ok := s.receiptDocumentRequest(w, r)
	if !ok {
		return
	}
	revision, present, err := httpio.SingleQueryValue(r, "revision")
	if err == nil && present && !receiptRevision.MatchString(revision) {
		err = &httpio.QueryParameterError{Parameter: "revision", Reason: "must be a receipt revision"}
	}
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	offset, _, err := httpio.OptionalIntQuery(r, "offset", 0, 0)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	limit, present, err := httpio.OptionalIntQuery(r, "limit", receiptDocumentLimit.Min, receiptDocumentLimit.Max)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	if !present {
		limit = receiptDocumentLimit.Default
	}
	page, err := reader.ReadReceiptDocument(r.Context(), chi.URLParam(r, "id"), scopes, revision, offset, limit)
	if err != nil {
		s.receiptDocumentError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, page)
}

func (s *Transcript) HandleSearchCommandReceiptDocument(w http.ResponseWriter, r *http.Request) {
	reader, scopes, ok := s.receiptDocumentRequest(w, r)
	if !ok {
		return
	}
	revision, _, err := httpio.SingleQueryValue(r, "revision")
	if err == nil && !receiptRevision.MatchString(revision) {
		err = &httpio.QueryParameterError{Parameter: "revision", Reason: "must be a receipt revision"}
	}
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	query, _, err := httpio.SingleQueryValue(r, "q")
	if err == nil && (query == "" || utf8.RuneCountInString(query) > 1024) {
		err = &httpio.QueryParameterError{Parameter: "q", Reason: "must hold 1 to 1024 characters"}
	}
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	sensitive, _, err := httpio.OptionalBoolQuery(r, "case_sensitive")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	page, err := httpio.ReadPageQuery(r, receiptSearchLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	result, err := reader.SearchReceiptDocument(r.Context(), chi.URLParam(r, "id"), scopes, invocation.ReceiptSearch{
		Revision: revision, Query: query, Cursor: page.Cursor, CaseSensitive: sensitive, Limit: page.Limit,
	})
	if err != nil {
		s.receiptDocumentError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}

func (s *Transcript) receiptDocumentRequest(w http.ResponseWriter, r *http.Request) (invocation.ProgramReader, []invocation.ReceiptScope, bool) {
	if _, err := s.Store.Get(r.Context(), chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
			return nil, nil, false
		}
		s.responses.InternalError(w, r, err)
		return nil, nil, false
	}
	reader, ok := s.Invocations.(invocation.ProgramReader)
	if !ok {
		s.responses.InternalError(w, r, errors.New("program receipt reader is unavailable"))
		return nil, nil, false
	}
	scopes, err := invocation.ParseReceiptScopes(r.URL.Query()["scopes"])
	if err != nil {
		s.receiptDocumentError(w, r, err)
		return nil, nil, false
	}
	return reader, scopes, true
}

func (s *Transcript) receiptDocumentError(w http.ResponseWriter, r *http.Request, err error) {
	var request *invocation.ReceiptRequestError
	switch {
	case errors.As(err, &request):
		s.responses.InvalidQueryParam(w, request.Parameter, request.Reason)
	case errors.Is(err, pagecursor.ErrInvalid), errors.Is(err, pagecursor.ErrExpired):
		s.responses.PageCursorError(w, r, "cursor", err)
	case errors.Is(err, invocation.ErrProgramNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeCommandProgramNotFound, "command program not found")
	case errors.Is(err, invocation.ErrReceiptChanged):
		s.responses.Fail(w, wire.ApiErrorCodeChatContentChanged, "This receipt changed. Reopen or refresh it to read the current version.")
	default:
		s.responses.InternalError(w, r, err)
	}
}
