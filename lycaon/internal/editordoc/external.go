package editordoc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
)

// PathRef addresses a document or subtree by root and root-relative path.
type PathRef struct {
	RootID string
	Path   string
}

// ObserveExternal imports outside changes through the filesystem replica. An
// empty selection reconciles resident and unsaved documents in the project.
func (s *Service) ObserveExternal(ctx context.Context, p *project.Project, selection []PathRef) error {
	if s == nil || s.store == nil || p == nil {
		return nil
	}
	return s.observeExternal(ctx, p, selection)
}

func (r externalReconciler) observeExternal(ctx context.Context, p *project.Project, selection []PathRef) error {
	s := r.s
	s.ops.RLock()
	defer s.ops.RUnlock()
	query, err := s.observationQuery(p, selection)
	if err != nil {
		return err
	}
	var failures []error
	after := ""
	for {
		page, err := s.store.observedDocumentPage(ctx, query, after)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		for _, d := range page {
			if err := s.observeExternalDocument(ctx, p, d.ID, selection); err != nil {
				failures = append(failures, fmt.Errorf("editor document %s: %w", d.ID, err))
			}
		}
		if len(page) < observationPageSize {
			return errors.Join(failures...)
		}
		after = page[len(page)-1].ID
	}
}

func (r externalReconciler) observeExternalDocument(ctx context.Context, p *project.Project, id string, selection []PathRef) error {
	s := r.s
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	d, err := s.store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(selection) > 0 {
		selected := false
		for _, ref := range selection {
			path := strings.TrimSpace(ref.Path)
			if strings.TrimSpace(ref.RootID) == d.RootID && (path == "." || path == d.Path || strings.HasPrefix(d.Path, path+"/")) {
				selected = true
				break
			}
		}
		if !selected {
			return nil
		}
	}
	return s.reconcileDocumentDisk(ctx, p, d, "")
}

// reconcileDocumentDisk is the one transition between a document and its
// file: a missing file marks the document absent; a present file is tracked
// and its bytes reconcile into the shared text. The caller holds the lock.
func (r externalReconciler) reconcileDocumentDisk(ctx context.Context, p *project.Project, d *Document, observerClientID string) error {
	s := r.s
	release, err := s.reserveDocumentSource(ctx, p, d)
	if errors.Is(err, projectsource.ErrSourceBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	observation, read, err := observeDocumentSource(p, d)
	if errors.Is(err, projectsource.ErrSourceNotFound) || errors.Is(err, os.ErrNotExist) {
		return s.markAbsent(ctx, d)
	}
	var unsupported *projectsource.SourceUnsupportedEncodingError
	if err != nil && !errors.As(err, &unsupported) {
		return err
	}
	fileID := ""
	if observation != nil {
		tracked, err := s.ledger.TrackFile(ctx, trackInput(p, observation))
		if err != nil {
			return err
		}
		fileID = tracked.FileID
	}
	return s.reconcileObservedDocument(ctx, d, read, unsupported != nil, observerClientID, fileID)
}

// markAbsent records that the path has no file; the draft and saved base stay.
func (r externalReconciler) markAbsent(ctx context.Context, d *Document) error {
	s := r.s
	if d.Absent {
		return nil
	}
	previous := d.Revision
	d.Absent = true
	d.Revision++
	d.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateCAS(ctx, d, previous); err != nil {
		return err
	}
	s.changed(ctx, d, false)
	return nil
}

// reconcileObservedDocument folds one observed file into the document. A
// recreated path rebinds the document to fileID and imports its bytes.
func (r externalReconciler) reconcileObservedDocument(ctx context.Context, d *Document, read *projectsource.SourceReadResult, unsupported bool, observerClientID, fileID string) error {
	s := r.s
	previous := d.Revision
	reappeared := d.Absent
	rebound := fileID != "" && fileID != d.FileID
	if rebound {
		d.FileID = fileID
	}
	d.Absent = false
	settled := !reappeared && !rebound
	switch {
	case unsupported:
		if d.Diverged && settled {
			return nil
		}
		d.Diverged = true
	case d.BaseSHA256 != read.SHA256:
		if read.Binary || read.OverLimit || read.Encoding == "" {
			if d.Diverged && settled {
				return nil
			}
			d.Diverged = true
			break
		}
		before := d.Draft
		if err := s.importPublishedText(ctx, d, read, filesystemActor); err != nil {
			return err
		}
		s.changed(ctx, d, before != d.Draft)
		return nil
	case d.Diverged:
		d.Diverged = false
	default:
		if settled {
			return nil
		}
	}
	d.Revision++
	d.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateCAS(ctx, d, previous); err != nil {
		return err
	}
	s.changed(ctx, d, false)
	return nil
}
