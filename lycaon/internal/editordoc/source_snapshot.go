package editordoc

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/project"
)

type SourceSnapshotMode uint8

const (
	// ObserveCurrent reads a draft when it has unpublished edits, otherwise disk.
	ObserveCurrent SourceSnapshotMode = iota
	// AdmitEditable reconciles and admits text for subsequent collaborative edits.
	AdmitEditable
)

// SourceSnapshot identifies the immutable text served from a resolved workspace.
// Document carries its pinned revision; Source carries the observed disk identity.
type SourceSnapshot struct {
	Document *Document
	Source   *project.SourceReadResult
}

// ResolveSourceSnapshot serves the pinned document when it holds unsaved edits or
// the mode admits it for editing, and the disk observation otherwise.
func (s *Service) ResolveSourceSnapshot(ctx context.Context, p *project.Project, req project.SourceReadRequest, mode SourceSnapshotMode) (*SourceSnapshot, error) {
	if mode == AdmitEditable {
		d, err := s.Open(ctx, p, req.Path, req.RootID, req.DecodeAs, "", nil)
		if err != nil {
			return nil, err
		}
		if err := s.trimReadDocuments(ctx, d.ID); err != nil {
			return nil, err
		}
		pinned, err := s.Pin(ctx, p.ID, d.ID, 0)
		return &SourceSnapshot{Document: pinned}, err
	}
	if s != nil && s.store != nil {
		d, err := s.store.GetByIdentity(ctx, p.ID, p.BranchForRoot(req.RootID), req.RootID, req.Path)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if err == nil && d.Dirty {
			// Zero chooses the current accepted revision under the document lock.
			pinned, pinErr := s.Pin(ctx, p.ID, d.ID, 0)
			if pinErr != nil {
				return nil, pinErr
			}
			if pinned.Dirty {
				return &SourceSnapshot{Document: pinned}, nil
			}
		}
	}
	observation, err := project.ObserveProjectSource(p, req)
	if err != nil {
		return nil, err
	}
	source, err := observation.Project()
	return &SourceSnapshot{Source: source}, err
}
