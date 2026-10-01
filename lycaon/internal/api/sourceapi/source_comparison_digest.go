package sourceapi

import (
	"context"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// sourceDigestWorkers bounds concurrent measurement within one request.
const sourceDigestWorkers = 4

// HandleDigestSourceComparisons measures comparisons without opening views.
// Nothing is decorated or retained.
func (s *Handler) HandleDigestSourceComparisons(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	var request wire.SourceComparisonDigestRequest
	if err := httpio.DecodeSourceJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if err := validateSourceComparisonDigestRequest(request); err != nil {
		s.writeSourceViewError(w, r, err)
		return
	}
	scoped, err := requestscope.ResolveSessionProject(s.SessionStore, r.Context(), p, request.SessionID)
	if err != nil {
		s.writeSourceViewError(w, r, err)
		return
	}
	// Measurements are shared within the caller's project, like the documents
	// a reader prepares.
	scope := pagedview.Scope{Person: requestscope.Caller(r).ID, Project: scoped.Project.ID}
	// One tree read per folder answers every commit comparison in the batch.
	trees := s.resolveCommitTrees(r.Context(), scoped.Project, request.Sources)
	digests := make([]wire.SourceComparisonDigest, len(request.Sources))
	next := make(chan int)
	var workers sync.WaitGroup
	for range min(sourceDigestWorkers, len(request.Sources)) {
		workers.Go(func() { //nolint:contextcheck // workers read the request context directly
			for index := range next {
				digests[index] = s.digestSourceComparison(r.Context(), scope, scoped.Project, request.SessionID, request.Sources[index], trees)
			}
		})
	}
	for index := range request.Sources {
		next <- index
	}
	close(next)
	workers.Wait()
	if err := r.Context().Err(); err != nil {
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceComparisonDigests{Digests: digests})
}

func validateSourceComparisonDigestRequest(request wire.SourceComparisonDigestRequest) error {
	if len(request.Sources) == 0 || len(request.Sources) > 64 {
		return rejectedSourceIntent("sources must name between 1 and 64 comparisons.")
	}
	if request.SessionID != "" {
		if _, err := uuid.Parse(request.SessionID); err != nil {
			return rejectedSourceIntent("session_id must be a UUID.")
		}
	}
	for _, source := range request.Sources {
		if err := source.Validate(); err != nil {
			return rejectedSourceIntent(err.Error())
		}
		// Retained and current sides live in a view; text sides are in the request.
		if source.Retained != nil || source.Current != nil || source.Text != nil {
			return rejectedSourceIntent("Retained, current and text comparisons are measured through a view.")
		}
	}
	return nil
}

func (s *Handler) digestSourceComparison(ctx context.Context, scope pagedview.Scope, p *project.Project, sessionID string, source wire.SourceComparisonSelector, trees *commitTrees) wire.SourceComparisonDigest {
	failed := func(err error) wire.SourceComparisonDigest {
		return wire.SourceComparisonDigest{Failure: sourcePreparationFailure(err, "The comparison could not be measured.")}
	}
	comparison, err := s.loadComparisonSource(ctx, p, sessionID, source, trees)
	if err != nil {
		return failed(err)
	}
	if !comparison.InRange || comparison.Before == nil || comparison.After == nil {
		return wire.SourceComparisonDigest{InRange: false}
	}
	// Digests measure files the reader has not opened, so they queue behind
	// the comparison on screen rather than competing with it. The measurement
	// is shared by content, so a file measured once is not measured again.
	measured, err := s.sourceReaders.Measure(ctx, scope.Person, scope.Project, *comparison.Before, *comparison.After,
		comparison.Attribution, "changes", backgroundwork.PriorityProactive)
	if err != nil {
		return failed(err)
	}
	return wire.SourceComparisonDigest{InRange: true, Summary: &measured.Summary, ChangesRows: measured.Extent.Rows, ChangesFolds: measured.Extent.Folds}
}
