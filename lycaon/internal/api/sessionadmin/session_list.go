package sessionadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/session/chats"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var sessionListLimit = httpio.MustPageLimit(wire.DefaultSessionListLimit, 1, wire.MaxSessionListLimit)

// HandleListProjectSessions lists top-level chats for one project.
func (s *Transcript) HandleListProjectSessions(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Projects, s.responses, w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	pq, queryErr := httpio.ReadPageQuery(r, sessionListLimit)
	if queryErr != nil {
		s.responses.InvalidQuery(w, queryErr)
		return
	}
	query := store.SummaryQuery{
		ProjectID:  p.ID,
		TitleQuery: strings.TrimSpace(q.Get("q")),
		Cursor:     pq.Cursor,
		Limit:      pq.Limit,
	}
	archived, _, queryErr := httpio.OptionalBoolQuery(r, "archived")
	if queryErr != nil {
		s.responses.InvalidQuery(w, queryErr)
		return
	}
	query.Archived = archived
	pinned, pinnedPresent, queryErr := httpio.OptionalBoolQuery(r, "pinned")
	if queryErr != nil {
		s.responses.InvalidQuery(w, queryErr)
		return
	}
	if pinnedPresent {
		query.Pinned = &pinned
	}
	switch sort := wire.SessionListSort(strings.TrimSpace(q.Get("sort"))); sort {
	case "", wire.SessionListSortActivity:
		query.Sort = wire.SessionListSortActivity
	case wire.SessionListSortCreated, wire.SessionListSortTitle:
		query.Sort = sort
	case wire.SessionListSortPin:
		if query.Pinned == nil || !*query.Pinned {
			s.responses.InvalidQueryParam(w, "sort", "pin requires pinned=true")
			return
		}
		query.Sort = sort
	default:
		s.responses.InvalidQueryParam(w, "sort", "must be activity, created, title, or pin")
		return
	}
	switch order := wire.SessionListOrder(strings.TrimSpace(q.Get("order"))); order {
	case "":
		// Recency sorts newest first; titles and pins read top down.
		if query.Sort == wire.SessionListSortTitle || query.Sort == wire.SessionListSortPin {
			query.Order = wire.SessionListOrderAsc
		} else {
			query.Order = wire.SessionListOrderDesc
		}
	case wire.SessionListOrderAsc, wire.SessionListOrderDesc:
		query.Order = order
	default:
		s.responses.InvalidQueryParam(w, "order", "must be asc or desc")
		return
	}

	page, err := s.Sessions.Chats.ListProjectSessions(r.Context(), query)
	if errors.Is(err, chats.ErrInvalidSessionListCursor) {
		s.responses.PageCursorError(w, r, "cursor", err)
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, page)
}
