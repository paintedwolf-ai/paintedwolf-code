package sourceapi

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type sourceViewSearchCursor struct {
	Revision string `json:"revision"`
	Row      int64  `json:"row"`
	Byte     int    `json:"byte,omitempty"`
}

var (
	sourceViewSearchPages = pagecursor.For[sourceViewSearchCursor]("source_view_search")
	sourceViewSearchLimit = httpio.MustPageLimit(pagedview.MaxRows, 1, pagedview.MaxRows)
)

type sourceViewSearchQuery struct {
	query     string
	page      httpio.PageQuery
	sensitive bool
}

func parseSourceViewSearch(r *http.Request) (sourceViewSearchQuery, error) {
	var out sourceViewSearchQuery
	const queryParam = "q"
	var err error
	out.query, _, err = httpio.SingleQueryValue(r, queryParam)
	if err != nil {
		return out, err
	}
	if out.query == "" || len(out.query) > 4096 || !utf8.ValidString(out.query) || strings.ContainsAny(out.query, "\r\n") {
		return out, &httpio.QueryParameterError{Parameter: queryParam, Reason: "must be one nonempty UTF-8 line of at most 4096 bytes"}
	}
	out.sensitive, _, err = httpio.OptionalBoolQuery(r, "case_sensitive")
	if err != nil {
		return out, err
	}
	out.page, err = httpio.ReadPageQuery(r, sourceViewSearchLimit)
	return out, err
}

// decodeSourceViewSearchCursor opens a search continuation; a tree view
// continues by row only, a comparison view by row and byte.
func decodeSourceViewSearchCursor(raw, scope string, tree bool) (sourceViewSearchCursor, error) {
	if raw == "" {
		return sourceViewSearchCursor{}, nil
	}
	cursor, err := sourceViewSearchPages.Decode(raw, scope)
	if err != nil {
		return sourceViewSearchCursor{}, err
	}
	if cursor.Row < 0 || cursor.Byte < 0 || (tree && cursor.Byte != 0) || int64(int(cursor.Row)) != cursor.Row {
		return sourceViewSearchCursor{}, pagecursor.ErrInvalid
	}
	return cursor, nil
}

func (s *Presentation) HandleSearchSourceView(w http.ResponseWriter, r *http.Request) {
	view, r, release, ok := s.requestedSourcePresentation(w, r)
	if !ok {
		return
	}
	defer release()
	query, err := parseSourceViewSearch(r)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	identity, _ := sourceViewCanonical(struct {
		View, Person, Query string
		Sensitive           bool
	}{view.id, view.scope.Person, query.query, query.sensitive})
	scope := string(identity)
	cursor, err := decodeSourceViewSearchCursor(query.page.Cursor, scope, view.navigation.tree != nil)
	if err != nil {
		s.responses.PageCursorError(w, r, "cursor", err)
		return
	}
	read := view
	var out wire.SourceViewSearchPage
	if view.navigation.tree != nil {
		out.Tree, err = read.searchTree(r, query, cursor, scope)
	} else {
		out.Comparison, err = read.searchComparison(r, query, cursor, scope)
	}

	if err != nil {
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (view *sourceViewRead) searchTree(r *http.Request, query sourceViewSearchQuery, cursor sourceViewSearchCursor, scope string) (*wire.SourceTreeSearchPage, error) {
	if view.reviewing.reviewPreparing {
		return nil, treeReviewPreparing()
	}
	var page sourcetree.SearchPage
	var err error
	if view.navigation.treeIntent.Filter != "" {
		if view.filtering.filtered == nil {
			return nil, &comparisonFailure{wire.ApiErrorCodeSourceViewPreparing, "The source filter is being prepared."}
		}
		page, err = view.filtering.filtered.Find(r.Context(), cursor.Revision, query.query, cursor.Row, query.page.Limit, query.sensitive)
	} else {
		page, err = view.presentation.Find(r.Context(), cursor.Revision, query.query, cursor.Row, query.page.Limit, query.sensitive)
	}
	if err != nil {
		return nil, err
	}
	out := &wire.SourceTreeSearchPage{Kind: "tree", ViewID: view.id, ProjectionRevision: page.Revision.Projection, Complete: page.Complete, Matches: []wire.SourceTreeSearchMatch{}}
	for _, match := range page.Matches {
		out.Matches = append(out.Matches, wire.SourceTreeSearchMatch{Address: wireTreeAddress(match.Address), Index: match.Index, From: match.From, To: match.To})
	}
	if !page.Complete {
		out.NextCursor, err = sourceViewSearchPages.Encode(scope, sourceViewSearchCursor{Revision: page.Revision.Projection, Row: page.Next})
	}
	return out, err
}

func (view *sourceViewRead) searchComparison(r *http.Request, query sourceViewSearchQuery, cursor sourceViewSearchCursor, scope string) (*wire.SourceComparisonSearchPage, error) {
	if view.state != "ready" {
		return nil, &comparisonFailure{wire.ApiErrorCodeSourceViewPreparing, "The source comparison is not ready."}
	}
	revision := viewProjectionRevision(view.intentRevision, view.projectionRevision)
	if cursor.Revision != "" && cursor.Revision != revision {
		return nil, pagedview.ErrRevision
	}
	out := &wire.SourceComparisonSearchPage{Kind: "comparison", ViewID: view.id, ProjectionRevision: revision, Complete: true, Matches: []wire.SourceReaderMatch{}}
	if view.comparisonData.comparison == nil && view.comparisonData.current == nil {
		return out, nil
	}
	var page sourcecomparison.SearchPage
	var err error
	start := sourcecomparison.SearchCursor{Row: int(cursor.Row), Byte: cursor.Byte}
	if view.comparisonData.current != nil {
		page, err = view.comparisonData.current.document.Find(r.Context(), query.query, start, query.page.Limit, query.sensitive)
	} else {
		page, err = view.comparisonData.comparison.Find(r.Context(), query.query, start, query.page.Limit, query.sensitive)
	}
	if err != nil {
		return nil, err
	}
	out.Complete, out.Matches = page.Complete, page.Matches
	if !page.Complete {
		out.NextCursor, err = sourceViewSearchPages.Encode(scope, sourceViewSearchCursor{Revision: revision, Row: int64(page.Next.Row), Byte: page.Next.Byte})
	}
	return out, err
}
