package invocation

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// reviewedOwnerStart joins screened input retention and the durable start marker.
type reviewedOwnerStart struct {
	programID, executionID string
	progress               bool
	record                 func(context.Context, *sql.Tx, string) error
}

func (p ProgramInputCapture) startReviewedOwner(ctx context.Context, recorder *SQLRecorder, start reviewedOwnerStart, review toolcontract.ReviewedOperationInputs) error {
	if p.Outputs == nil || p.Outputs.recorder != recorder || recorder == nil || recorder.database == nil {
		return fmt.Errorf("owner start requires the capture's invocation recorder")
	}
	_, err := p.retain(ctx, start.executionID, review, func(reference string, spill *tooloutput.StreamSpill, redacted bool) (*tooloutput.RetainedStreamBody, error) {
		return spill.TransferBody(ctx, func(ctx context.Context, body tooloutput.RetainedStreamReference) error {
			if body.InitiatingClaimID != start.executionID {
				return fmt.Errorf("reviewed input provenance differs from owner execution")
			}
			return recorder.commitReviewedOwner(ctx, start, reference, func(tx *sql.Tx) error {
				childID := start.executionID
				if start.progress {
					childID = ""
				}
				output := RetainedOutput{ExecutionID: childID, Stream: "arguments", Reference: reference, Complete: true, Redacted: redacted}
				return insertProgramOutput(ctx, tx, start.programID, output, body, "[]", nil)
			})
		})
	}, func(reference string, body []byte, redacted bool) error {
		p.Outputs.mu.Lock()
		defer p.Outputs.mu.Unlock()
		if !p.Outputs.ready || p.Outputs.release == nil {
			return fmt.Errorf("program output recovery is incomplete")
		}
		return recorder.commitReviewedOwner(ctx, start, reference, func(tx *sql.Tx) error {
			childID := start.executionID
			if start.progress {
				childID = ""
			}
			return insertInlineProgramInput(ctx, tx, start.programID, childID, start.executionID, reference, body, redacted)
		})
	})
	return err
}

func (r *SQLRecorder) commitReviewedOwner(ctx context.Context, start reviewedOwnerStart, reference string, attach func(*sql.Tx) error) error {
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := attach(tx); err != nil {
		return err
	}
	if err := start.record(ctx, tx, reference); err != nil {
		return err
	}
	return tx.Commit()
}
