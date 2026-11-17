package editordoc

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/projectsource"
)

// ErrExecutionConflict reports that the file a program's change was prepared
// against changed outside the document before acceptance, or that the
// document fell behind the program's capture. The change is not rerun.
var ErrExecutionConflict = errors.New("document no longer matches the program's execution pin")

// ExecutionPin is the accepted revision and published base a program
// operation captured before preparing its change. The change is anchored in
// that revision, so typing that arrives after the capture merges with it the
// way a conversational edit does; it needs no conversational read.
type ExecutionPin struct {
	Revision int64
	// DiskSHA256 is the published file the accepted revision sits on.
	DiskSHA256 string
	Absent     bool
}

// ExecutionConflict names the pinned and current states of one document.
type ExecutionConflict struct {
	DocumentID   string
	RootID, Path string
	Pinned       ExecutionPin
	Current      ExecutionPin
}

func (e *ExecutionConflict) Error() string {
	return fmt.Sprintf("%s: document %s pinned at revision %d on %q, now revision %d on %q", ErrExecutionConflict, e.DocumentID, e.Pinned.Revision, e.Pinned.DiskSHA256, e.Current.Revision, e.Current.DiskSHA256)
}

func (e *ExecutionConflict) Unwrap() error { return ErrExecutionConflict }

// admitExecutionPin admits a program edit while the document is at or past its
// capture and the file on disk is still the one the document sits on. A save
// through the document moves both together and is no conflict; a file changed
// outside the document is. The caller holds the document lock, so acceptance
// follows this check without another writer in between; save publication
// still compares the disk bytes again.
func (s *Service) admitExecutionPin(ctx context.Context, d *Document, in AgentEdit) error {
	if in.Pin == nil {
		return nil
	}
	if in.ExpectedRevision != in.Pin.Revision || (in.ReviewedRevision != 0 && in.ReviewedRevision < in.Pin.Revision) {
		return ErrOperationConflict
	}
	current := ExecutionPin{Revision: d.Revision, DiskSHA256: d.BaseSHA256, Absent: d.Absent}
	if d.Absent {
		current.DiskSHA256 = ""
	}
	if current.Revision < in.Pin.Revision || d.Diverged {
		return &ExecutionConflict{DocumentID: d.ID, RootID: d.RootID, Path: d.Path, Pinned: *in.Pin, Current: current}
	}
	p, err := s.projectBranch(ctx, d.ProjectID, d.BranchID)
	if err != nil {
		return err
	}
	disk := current
	read, err := readDocumentSource(p, d)
	switch {
	case errors.Is(err, projectsource.ErrSourceNotFound):
		disk.Absent, disk.DiskSHA256 = true, ""
	case err != nil:
		return err
	default:
		disk.Absent, disk.DiskSHA256 = false, read.SHA256
	}
	if disk.Absent != current.Absent || disk.DiskSHA256 != current.DiskSHA256 {
		return &ExecutionConflict{DocumentID: d.ID, RootID: d.RootID, Path: d.Path, Pinned: *in.Pin, Current: disk}
	}
	return nil
}
