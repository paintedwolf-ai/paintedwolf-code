package searchadmin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/search"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	var req wire.SearchRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "query is required")
		return
	}
	limit := req.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "limit must be between 1 and 500")
		return
	}
	scope, err := searchRequestScope(req)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if req.Cursor != "" {
		var response wire.SearchResponse
		generation, offset, err := globalSearchPages.DecodeAt(req.Cursor, scope, func(generation uint64) bool {
			var retained bool
			response, retained = s.searchPages.get(generation, scope)
			return retained
		})
		if err == nil && offset < 0 {
			err = pagecursor.ErrInvalid
		}
		if err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		page, err := searchResponsePage(response, generation, offset, limit, scope)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		httpio.WriteJSON(w, http.StatusOK, page)
		return
	}
	svc := s.searchService()
	origin := strings.TrimSpace(req.OriginProjectID)
	if !s.searchOriginExists(w, r, origin) {
		return
	}
	budget, ok := search.ParseSearchBudget(string(req.Budget))
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "budget must be interactive or complete")
		return
	}
	compileCtx := s.searchCompileContext(r.Context(), origin)
	compileCtx.IncludeDependencies = req.IncludeDependencies
	compileCtx.Flags = matchFlagsFromSearchRequest(req)
	compileCtx.Budget = budget
	result, err := svc.Search(r.Context(), query, compileCtx)
	if err != nil {
		s.writeSearchError(w, r, err)
		return
	}
	s.enrichSearchProjectNames(r.Context(), result)
	s.enrichSearchWorkerContext(r.Context(), result)
	response := toWireSearchResponse(result)
	generation := s.searchPages.put(scope, response)
	page, err := searchResponsePage(response, generation, 0, limit, scope)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, page)
}

// writeSearchError exposes structured query failures.
func (s *Handler) writeSearchError(w http.ResponseWriter, r *http.Request, err error) {
	if s.writeSearchQueryError(w, err) {
		return
	}
	s.responses.InternalError(w, r, err)
}

// writeSearchQueryError reports whether it handled the error.
func (s *Handler) writeSearchQueryError(w http.ResponseWriter, err error) bool {
	pe := &search.ParseError{}
	if errors.As(err, &pe) {
		s.responses.FailDetails(w, wire.ApiErrorCodeSearchQueryInvalid, map[string]any{
			"reason": pe.Error(),
			"offset": pe.Offset,
			"field":  pe.Field,
			"kind":   string(pe.Kind),
		}, pe.Error())
		return true
	}
	var me *search.MatchError
	if errors.As(err, &me) {
		s.responses.FailDetails(w, wire.ApiErrorCodeSearchPatternInvalid, map[string]any{
			"reason": me.Message,
		}, me.Message)
		return true
	}
	return false
}

// searchOriginExists validates the optional origin project.
func (s *Handler) searchOriginExists(w http.ResponseWriter, r *http.Request, origin string) bool {
	if origin == "" {
		return true
	}
	if _, err := s.projectRegistry.Get(r.Context(), origin); err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeProjectNotFound, "origin project not found")
		return false
	}
	return true
}

// globalSearchPages continues a search inside the cached result generation
// that answered its first page; the position is a hit offset.
var globalSearchPages = pagecursor.For[int]("global_search")

func searchRequestScope(req wire.SearchRequest) (string, error) {
	req.Cursor = ""
	req.Limit = 0
	raw, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("encode search scope: %w", err)
	}
	return string(raw), nil
}

func searchResponsePage(response wire.SearchResponse, generation uint64, offset, limit int, scope string) (wire.SearchResponse, error) {
	response.NextCursor = ""
	total := len(response.Hits)
	if offset >= total {
		response.Hits = []wire.SearchHit{}
		return response, nil
	}
	end := min(offset+limit, total)
	response.Hits = append([]wire.SearchHit(nil), response.Hits[offset:end]...)
	if end < total {
		var err error
		response.NextCursor, err = globalSearchPages.EncodeAt(scope, generation, end)
		if err != nil {
			return wire.SearchResponse{}, err
		}
	}
	return response, nil
}
