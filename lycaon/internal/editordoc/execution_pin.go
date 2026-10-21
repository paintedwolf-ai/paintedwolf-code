package editordoc

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/projectsource"
)

// ErrExecutionConflict reports that a program's captured document or disk
// state moved before acceptance. The prepared change is not anchored, rebased
// or rerun against the newer state.
var ErrExecutionConflict = errors.New("document no longer matches the program's execution pin")

// ExecutionPin is the accepted revision and published base a program
// operation captured before preparing its change. An edit carrying a pin is
// admitted only at exactly that state; it needs no conversational read.
type ExecutionPin struct {
	Revision int64
	// DiskSHA256 is the published file the accepted revision sits on.
	DiskSHA256 string
	Absent     bool
}

// ExecutionConflict names the pinned and current states of one document.
type ExecutionConflict struct {
	DocumentID string
	Pinned     ExecutionPin
	Current    ExecutionPin
}

func (e *ExecutionConflict) Error() string {
	return fmt.Sprintf("%s: document %s pinned at revision %d, now %d", ErrExecutionConflict, e.DocumentID, e.Pinned.Revision, e.Current.Revision)
}

func (e *ExecutionConflict) Unwrap() error { return ErrExecutionConflict }

// admitExecutionPin compares the document head and the file it publishes to
// with the capture. The caller holds the document lock, so acceptance follows
// this check without another writer in between; save publication still
// compares the disk bytes again.
func (s *Service) admitExecutionPin(ctx context.Context, d *Document, in AgentEdit) error {
	if in.Pin == nil {
		return nil
	}
	if in.ExpectedRevision != in.Pin.Revision || (in.ReviewedRevision != 0 && in.ReviewedRevision != in.Pin.Revision) {
		return ErrOperationConflict
	}
	current := ExecutionPin{Revision: d.Revision, DiskSHA256: d.BaseSHA256, Absent: d.Absent}
	if d.Absent {
		current.DiskSHA256 = ""
	}
	if current != *in.Pin {
		return &ExecutionConflict{DocumentID: d.ID, Pinned: *in.Pin, Current: current}
	}
	p, err := s.projectBranch(ctx, d.ProjectID, d.BranchID)
	if err != nil {
		return err
	}
	read, err := readDocumentSource(p, d)
	switch {
	case errors.Is(err, projectsource.ErrSourceNotFound):
		current.Absent, current.DiskSHA256 = true, ""
	case err != nil:
		return err
	default:
		current.Absent, current.DiskSHA256 = false, read.SHA256
	}
	if current != *in.Pin {
		return &ExecutionConflict{DocumentID: d.ID, Pinned: *in.Pin, Current: current}
	}
	return nil
}

// PublishedThrough reports whether the file holds the given accepted revision,
// as a save of it or of a later revision. It never saves anything.
func (a *AgentService) PublishedThrough(ctx context.Context, projectID, documentID string, revision int64) (bool, *Document, error) {
	d, err := a.s.checked(ctx, documentID, projectID)
	if err != nil {
		return false, nil, err
	}
	head, err := a.s.store.replicaHead(ctx, d.ID)
	if err != nil {
		return false, nil, err
	}
	d.PublishedRevision = head.PublishedRevision
	return d.PublishedRevision >= revision && !d.Diverged && d.HeldAgentVersionID == "", d, nil
}
