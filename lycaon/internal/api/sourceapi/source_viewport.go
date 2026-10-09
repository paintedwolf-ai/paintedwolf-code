package sourceapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/pagedview"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type sourceFrameKey struct {
	presentation string
	offset       int64
	limit        int
}

func (s *Presentation) HandleReplaceSourceViewportInterest(w http.ResponseWriter, r *http.Request) {
	var demand wire.SourceViewportInterest
	if err := httpio.DecodeJSON(w, r, &demand); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	id, err := sourceHandleParam(r, "interest_id")
	if err != nil {
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	if _, err := uuid.Parse(demand.PresentationID); err != nil || demand.Sequence < 1 || demand.Sequence > 9007199254740991 || demand.Start < 0 || demand.End <= demand.Start || demand.End-demand.Start > 4*pagedview.MaxRows || demand.Direction < -1 || demand.Direction > 1 {
		s.Views.writeSourceViewError(w, r, pagedview.ErrRange)
		return
	}
	view, r, releaseView, ok := s.Views.requestedSourceView(w, r)
	if !ok {
		return
	}
	defer releaseView()
	service := s.Views.sourceViewRegistry()
	presentation, release, err := s.acquireBasisPresentation(view, demand.PresentationID)
	if err != nil {
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	total := int64(0)
	if presentation.summary.Tree != nil {
		total = presentation.summary.Tree.Extent.Rows
	} else if presentation.summary.Comparison != nil {
		total = presentation.summary.Comparison.Extent.Rows
	}
	pages := pagedview.ViewportPages(demand.Start, demand.End, total, demand.Direction)
	if err := r.Context().Err(); err != nil {
		release()
		s.responses.InternalError(w, r, err)
		return
	}
	//nolint:contextcheck // Viewport demand outlives its HTTP acknowledgement.
	err = view.interests.Replace(view.ctx, id, demand.Sequence, func(ctx context.Context) {
		for _, offset := range pages {
			select {
			case <-ctx.Done():
				return
			case service.frameWorkers <- struct{}{}:
			}
			_, err := s.sourceFrame(r.WithContext(ctx), demand.PresentationID, presentation.read, sourceFrameQuery{offset: offset, limit: pagedview.MaxRows})
			<-service.frameWorkers
			if err != nil {
				return
			}
		}
	}, release)
	if err != nil {
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Presentation) HandleReleaseSourceViewportInterest(w http.ResponseWriter, r *http.Request) {
	view, r, release, ok := s.Views.requestedSourceView(w, r)
	if !ok {
		return
	}
	defer release()
	view.interests.Release(chi.URLParam(r, "interest_id"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Presentation) sourceFrame(r *http.Request, id string, read *sourceViewRead, query sourceFrameQuery) ([]byte, error) {
	service := s.Views.sourceViewRegistry()
	key := sourceFrameKey{id, query.offset, query.limit}
	cacheable := query.anchor == "" && len(query.retain) == 0
	if cacheable {
		service.frameMu.Lock()
		cached, found := service.frames.Get(key)
		service.frameMu.Unlock()
		if found {
			return cached, nil
		}
	}
	var frame wire.SourceViewFrame
	var err error
	if read.navigation.tree != nil {
		frame.Tree, err = read.treeFrame(r, query)
	} else {
		frame.Comparison, err = read.comparisonFrame(r, query)
	}
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(frame)
	if err == nil && cacheable && r.Context().Err() == nil {
		service.frameMu.Lock()
		service.frames.Put(key, encoded, int64(len(encoded)+len(id)+64))
		service.frameMu.Unlock()
	}
	return encoded, err
}
