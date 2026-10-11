package sourceapi

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/pagedview"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type sourcePresentation struct {
	read        *sourceViewRead
	summary     wire.SourceView
	release     func()
	releaseView func()
}

func (p *sourcePresentation) close() {
	if p.read.presentation != nil {
		p.read.presentation.Close()
	}
	p.release()
	p.releaseView()
}

//nolint:contextcheck // Access checks derive the request context.
func (s *Presentation) HandleCreateSourcePresentation(w http.ResponseWriter, r *http.Request) {
	var request wire.SourcePresentationCreate
	if err := httpio.DecodeJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if err := validateSourceCommandIdentity(request.OperationID, request.IntentRevision); err != nil {
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	view, r, release, ok := s.Views.requestedSourceView(w, r)
	if !ok {
		return
	}
	defer release()
	service := s.Views.sourceViewRegistry()
	view.presentationMu.Lock()
	defer view.presentationMu.Unlock()
	canonical, _ := sourceViewCanonical(request)
	digest := sha256.Sum256(canonical)
	namespace := "presentation:" + view.id
	saved, found, err := service.receipts.Lookup(r.Context(), namespace, request.OperationID)
	if err != nil {
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	if found {
		if !bytes.Equal(saved.Digest, digest[:]) {
			s.Views.writeSourceViewError(w, r, pagedview.ErrOperationConflict)
			return
		}
		retained, unpin, err := service.presentations.Acquire(view.scope, string(saved.Value))
		if err != nil {
			s.Views.writeSourceViewError(w, r, err)
			return
		}
		defer unpin()
		httpio.WriteJSON(w, http.StatusCreated, wire.SourcePresentation{ID: string(saved.Value), View: retained.summary})
		return
	}
	if request.IntentRevision != view.commands.Revision() {
		s.Views.writeSourceViewError(w, r, pagedview.ErrRevision)
		return
	}
	read, releaseRead := view.read()
	if read.navigation.tree != nil && read.navigation.treeIntent.Filter == "" && !read.reviewing.reviewPreparing {
		read.presentation, err = read.navigation.tree.Capture(r.Context())
	}
	var summary wire.SourceView
	if err == nil {
		summary, err = read.snapshot(r.Context())
	}
	if err == nil {
		err = read.validate()
	}
	if err == nil && (summary.Tree != nil && summary.Tree.State != "ready" || summary.Comparison != nil && summary.Comparison.State != "ready") {
		err = pagedview.ErrPreparing
	}
	if err != nil {
		if read.presentation != nil {
			read.presentation.Close()
		}
		releaseRead()
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	if summary.Tree != nil {
		read.navigation.treeIntent = summary.Tree.Intent
	}
	_, releaseView, err := service.registry.Acquire(view.scope, view.id)
	if err != nil {
		if read.presentation != nil {
			read.presentation.Close()
		}
		releaseRead()
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	retained := &sourcePresentation{read: read, summary: summary, release: releaseRead, releaseView: releaseView}
	if err = service.receipts.Reserve(r.Context(), namespace, request.OperationID, digest[:]); err != nil {
		retained.close()
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	encoded, _ := sourceViewCanonical(summary)
	charge := sourceViewDescriptorBytes + int64(2*len(encoded))
	if read.presentation != nil {
		charge += read.presentation.Bytes()
	}
	id, err := service.presentations.Put(view.scope, retained, charge, (*sourcePresentation).close)
	if err != nil {
		retained.close()
		_ = service.receipts.Abort(r.Context(), namespace, request.OperationID)
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	if err = service.receipts.Complete(r.Context(), namespace, request.OperationID, []byte(id)); err != nil {
		service.presentations.Release(view.scope, id)
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, wire.SourcePresentation{ID: id, View: summary})
}

// A presentation belongs to exactly one view; under any other view it is not retained.
func (s *Presentation) acquireViewPresentation(view *sourceView, id string) (*sourcePresentation, func(), error) {
	retained, release, err := s.Views.sourceViewRegistry().presentations.Acquire(view.scope, id)
	if err != nil {
		return nil, nil, err
	}
	if retained.read.id != view.id {
		release()
		return nil, nil, pagedview.ErrExpired
	}
	return retained, release, nil
}

// A basis named in a request body belongs to a healthy view; when that view no
// longer presents it, the coordinates changed. Expiry would make the client
// replace the view.
func (s *Presentation) acquireBasisPresentation(view *sourceView, id string) (*sourcePresentation, func(), error) {
	retained, release, err := s.acquireViewPresentation(view, id)
	if errors.Is(err, pagedview.ErrExpired) {
		return nil, nil, pagedview.ErrRevision
	}
	return retained, release, err
}

func (s *Presentation) requestedSourcePresentation(w http.ResponseWriter, r *http.Request) (*sourceViewRead, *http.Request, func(), bool) {
	view, r, releaseView, ok := s.Views.requestedSourceView(w, r)
	if !ok {
		return nil, r, nil, false
	}
	id, err := sourceHandleParam(r, "presentation_id")
	if err != nil {
		releaseView()
		s.Views.writeSourceViewError(w, r, err)
		return nil, r, nil, false
	}
	retained, release, err := s.acquireViewPresentation(view, id)
	if err != nil {
		releaseView()
		s.Views.writeSourceViewError(w, r, err)
		return nil, r, nil, false
	}
	return retained.read, r, func() { release(); releaseView() }, true
}

// HandleReleaseSourcePresentation is idempotent.
func (s *Presentation) HandleReleaseSourcePresentation(w http.ResponseWriter, r *http.Request) {
	view, r, releaseView, ok := s.Views.requestedSourceView(w, r)
	if !ok {
		return
	}
	defer releaseView()
	id, err := sourceHandleParam(r, "presentation_id")
	if err != nil {
		s.Views.writeSourceViewAccessError(w, r, err)
		return
	}
	_, release, err := s.acquireViewPresentation(view, id)
	if errors.Is(err, pagedview.ErrExpired) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.Views.writeSourceViewAccessError(w, r, err)
		return
	}
	defer release()
	s.Views.sourceViewRegistry().presentations.Release(view.scope, id)
	w.WriteHeader(http.StatusNoContent)
}
