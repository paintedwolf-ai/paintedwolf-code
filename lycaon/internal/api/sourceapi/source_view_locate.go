package sourceapi

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Presentation) HandleLocateSourceView(w http.ResponseWriter, r *http.Request) {
	view, r, release, ok := s.requestedSourcePresentation(w, r)
	if !ok {
		return
	}
	defer release()
	encoded, _, err := httpio.SingleQueryValue(r, "anchor")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	line, hasLine, err := httpio.OptionalIntQuery(r, "line", 1, 0)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	side, hasSide, err := httpio.SingleQueryValue(r, "side")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	switch {
	case encoded == "" && !hasLine, encoded != "" && hasLine:
		s.responses.InvalidQueryParam(w, "anchor", "supply an anchor, or a comparison line and side")
		return
	case hasLine && !hasSide, hasSide && side != "before" && side != "after":
		s.responses.InvalidQueryParam(w, "side", "must be before or after for a comparison line")
		return
	case hasSide && !hasLine:
		s.responses.InvalidQueryParam(w, "line", "is required with side")
		return
	}
	read := view
	var result wire.SourceViewLocation
	if view.navigation.tree != nil {
		if hasLine {
			err = pagedview.ErrRange
		} else {
			result.Tree, err = read.locateTree(r, encoded)
		}
	} else {
		result.Comparison, err = read.locateComparison(r, encoded, line, side)
	}

	if err != nil {
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}

func (view *sourceViewRead) locateTree(r *http.Request, encoded string) (*wire.SourceTreeLocation, error) {
	if view.reviewing.reviewPreparing {
		return nil, treeReviewPreparing()
	}
	anchor, err := decodeSourceAnchor[wire.SourceTreeAddress](encoded, "root_id", "path")
	if err != nil {
		return nil, err
	}
	if anchor == nil {
		return nil, pagedview.ErrRange
	}
	var location sourcetree.Location
	var revision pagedview.Revision
	if view.navigation.treeIntent.Filter != "" {
		if view.filtering.filtered == nil {
			return nil, &comparisonFailure{wire.ApiErrorCodeSourceViewPreparing, "The source filter is being prepared."}
		}
		location, err = view.filtering.filtered.Locate(r.Context(), treeAddress(*anchor))
		revision, _ = view.filtering.filtered.Revision()
	} else {
		location, revision, err = view.presentation.Locate(r.Context(), treeAddress(*anchor))
	}
	if err != nil {
		return nil, err
	}
	return &wire.SourceTreeLocation{Kind: "tree", ViewID: view.id, ProjectionRevision: revision.Projection, Index: location.Index,
		Visible: location.Visible, Pending: location.Pending, Address: wireTreeAddress(location.Address)}, nil
}

func (view *sourceViewRead) locateComparison(r *http.Request, encoded string, line int, side string) (*wire.SourceComparisonLocation, error) {
	if view.state != "ready" {
		return nil, &comparisonFailure{wire.ApiErrorCodeSourceViewPreparing, "The source comparison is not ready."}
	}
	if view.comparisonData.comparison == nil && view.comparisonData.current == nil || view.comparisonData.projection == nil {
		return nil, pagedview.ErrRange
	}
	anchor, err := decodeSourceAnchor[wire.SourceComparisonAnchor](encoded, "row")
	if err != nil {
		return nil, err
	}
	var sourceRow int
	if anchor != nil {
		sourceRow = anchor.Row
	} else if view.comparisonData.current != nil {
		sourceRow, err = view.comparisonData.current.document.RowAtLine(r.Context(), line)
		if err != nil {
			return nil, err
		}
	} else {
		sourceRow = view.comparisonData.comparison.RowAtLine(line, side)
	}
	rank, resolved, err := view.comparisonData.projection.Locate(r.Context(), sourceRow)
	if err != nil {
		return nil, err
	}
	return &wire.SourceComparisonLocation{Kind: "comparison", ViewID: view.id, ProjectionRevision: viewProjectionRevision(view.intentRevision, view.projectionRevision),
		Index: rank, Visible: resolved.Row == sourceRow, Anchor: wire.SourceComparisonAnchor{Row: sourceRow}}, nil
}
