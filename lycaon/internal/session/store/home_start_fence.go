package store

import (
	"context"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/timelayout"
)

// ErrHomeStartOperationClosed refuses a first prompt whose Home start was
// stopped or retired, or whose operation was replaced.
var ErrHomeStartOperationClosed = errors.New("home start operation is closed")

// ErrHomeStartOperationMismatch refuses a reserved first-prompt operation sent
// to another chat or by another person.
var ErrHomeStartOperationMismatch = errors.New("home start operation belongs to another chat")

// fenceHomeStartOperation admits a reserved first-prompt operation only into
// its bound chat, from its owner, while the start is active. It runs in the
// receipt transaction, so a concurrent Stop or replacement either precedes
// the receipt or observes it.
func fenceHomeStartOperation(ctx context.Context, q *db.Queries, in PromptSubmission, now time.Time) error {
	op, err := q.GetHomeStartOperation(ctx, in.ID)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if op.State != "current" {
		return ErrHomeStartOperationClosed
	}
	start, err := q.GetHomeStart(ctx, op.LaunchID)
	if err != nil {
		return err
	}
	if start.Disposition != "active" {
		return ErrHomeStartOperationClosed
	}
	if in.Origin != PromptSubmissionOriginUser || start.Progress != "session_bound" ||
		start.SessionID != in.SessionID || start.PersonID != in.SubmittedBy {
		return ErrHomeStartOperationMismatch
	}
	_, err = q.TouchHomeStartRevision(ctx, db.TouchHomeStartRevisionParams{
		UpdatedAt: timelayout.Format(now), LaunchID: start.LaunchID,
	})
	return err
}
