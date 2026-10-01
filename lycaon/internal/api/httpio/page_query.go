package httpio

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/lycaon/lycaon/internal/pagecursor"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// PageLimit is a list's declared `limit` bounds, matching its OpenAPI minimum
// and maximum. An out-of-range limit is rejected, never clamped.
type PageLimit struct {
	Default, Min, Max int
}

// MustPageLimit declares bounds at package init; inconsistent bounds panic.
func MustPageLimit(defaultLimit, minLimit, maxLimit int) PageLimit {
	if minLimit < 1 || maxLimit < minLimit || defaultLimit < minLimit || defaultLimit > maxLimit {
		panic(fmt.Sprintf("httpio: page limit default %d outside [%d, %d]", defaultLimit, minLimit, maxLimit))
	}
	return PageLimit{Default: defaultLimit, Min: minLimit, Max: maxLimit}
}

func (b PageLimit) read(r *http.Request) (int, error) {
	limit, present, err := OptionalIntQuery(r, "limit", b.Min, b.Max)
	if err != nil {
		return 0, err
	}
	if !present {
		return b.Default, nil
	}
	return limit, nil
}

// PageQuery is a forward page request: `cursor` (empty on the first page) and `limit`.
type PageQuery struct {
	Cursor string
	Limit  int
}

// ReadPageQuery parses `cursor` and `limit`. Errors are *QueryParameterError
// naming the parameter; answer them with Responder.InvalidQuery.
func ReadPageQuery(r *http.Request, bounds PageLimit) (PageQuery, error) {
	cursor, _, err := SingleQueryValue(r, "cursor")
	if err != nil {
		return PageQuery{}, err
	}
	limit, err := bounds.read(r)
	if err != nil {
		return PageQuery{}, err
	}
	return PageQuery{Cursor: cursor, Limit: limit}, nil
}

// WindowQuery is a bidirectional window request: at most one of `before` or
// `after` (both empty opens the newest window) and `limit`.
type WindowQuery struct {
	Before, After string
	Limit         int
}

// ReadWindowQuery parses `before`, `after`, and `limit`. Errors are
// *QueryParameterError naming the parameter; answer them with Responder.InvalidQuery.
func ReadWindowQuery(r *http.Request, bounds PageLimit) (WindowQuery, error) {
	before, _, err := SingleQueryValue(r, "before")
	if err != nil {
		return WindowQuery{}, err
	}
	after, _, err := SingleQueryValue(r, "after")
	if err != nil {
		return WindowQuery{}, err
	}
	if before != "" && after != "" {
		return WindowQuery{}, &QueryParameterError{Parameter: "after", Reason: "must not be combined with before"}
	}
	limit, err := bounds.read(r)
	if err != nil {
		return WindowQuery{}, err
	}
	return WindowQuery{Before: before, After: after, Limit: limit}, nil
}

// PageCursorError answers a pagecursor decode failure for the named query
// parameter: ErrExpired is cursor_generation_expired, ErrInvalid is
// invalid_page_cursor, anything else is an internal error.
func (s *Responder) PageCursorError(w http.ResponseWriter, r *http.Request, param string, err error) {
	switch {
	case errors.Is(err, pagecursor.ErrExpired):
		s.FailDetails(w, wire.ApiErrorCodeCursorGenerationExpired, map[string]any{"param": param},
			"the list changed since this cursor was issued; restart from the first page")
	case errors.Is(err, pagecursor.ErrInvalid):
		s.FailDetails(w, wire.ApiErrorCodeInvalidPageCursor, map[string]any{"param": param},
			"the cursor is not valid for this list")
	default:
		s.InternalError(w, r, err)
	}
}
