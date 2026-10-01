package editordoc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/textfile"
)

// settleRejectedReservation ends a save that preflight definitely rejected,
// recording the rejection before releasing its reserved bytes. Uncertain storage
// failures leave the reservation intact for automatic replay.
func (s *Service) settleRejectedReservation(ctx context.Context, d *Document, operationID string, actor saveActor, cause error) error {
	if !savePreflightRejected(cause) {
		return nil
	}
	if _, err := s.store.Mutation(ctx, operationID); err == nil {
		return nil // Publication already transferred the reservation to its journal.
	} else if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("read save rejection journal: %w", err)
	}
	now := time.Now().UTC()
	mutation := &Mutation{ID: operationID, DocumentID: d.ID, ProjectID: d.ProjectID, BranchID: d.BranchID,
		FileID: d.FileID, RootID: d.RootID, Path: d.Path, ClientID: actor.ClientID,
		InputDigest: saveInputDigest(d.ID, d.ProjectID, actor, d.Revision), DraftRevision: d.Revision,
		ExpectedSHA256: d.BaseSHA256, Encoding: d.Encoding, EOL: d.EOL,
		Status: "failed", Error: cause.Error(), SessionID: actor.SessionID, Turn: actor.Turn,
		Origin: actor.Origin, PersonID: actor.PersonID, CreatedAt: now, UpdatedAt: now}
	if err := s.store.InsertMutation(ctx, mutation); err != nil {
		return fmt.Errorf("preserve save rejection: %w", err)
	}
	return nil
}

func savePreflightRejected(err error) bool {
	var rejected *documentcore.Rejected
	return errors.As(err, &rejected) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrRevisionConflict) || errors.Is(err, ErrRootDetached) ||
		errors.Is(err, project.ErrSourceNotFound) || errors.Is(err, project.ErrSourcePathDenied) || errors.Is(err, project.ErrSourceWriteTooLarge) ||
		errors.Is(err, textfile.ErrRawTooLarge) || errors.Is(err, textfile.ErrTextTooLarge) || errors.Is(err, textfile.ErrBinary) || errors.Is(err, textfile.ErrUnsupported)
}
