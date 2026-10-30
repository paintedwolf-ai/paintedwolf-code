package invocation

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ProgramAdmissionRecorder commits screened inputs before owner dispatch.
type ProgramAdmissionRecorder struct {
	Recorder           *SQLRecorder
	Inputs             ProgramInputCapture
	OwnerStartRecorded bool
}

func (r *ProgramAdmissionRecorder) BeforeNativeOwner(ctx context.Context, bound toolcontract.BoundOperation, modelCallID string, inputs toolcontract.ReviewedOperationInputs) error {
	if r.Recorder == nil || r.Inputs.Outputs == nil || !validDigest(inputs.ArgumentsDigest()) {
		return fmt.Errorf("program start requires screened reviewed-input retention")
	}
	if err := validateReviewedInputs(inputs); err != nil {
		return err
	}
	in := bound.Invocation()
	if err := r.Inputs.validateNativeScope(in); err != nil {
		return err
	}
	start := reviewedOwnerStart{programID: in.Identity.ProgramID, executionID: in.Identity.ExecutionID}
	start.record = func(ctx context.Context, tx *sql.Tx, reference string) error {
		return r.Recorder.startNativeOwner(ctx, tx, bound, modelCallID, inputs.ArgumentsDigest(), reference, inputs.Representation())
	}
	if err := r.Inputs.startReviewedOwner(ctx, r.Recorder, start, inputs); err != nil {
		return err
	}
	r.OwnerStartRecorded = true
	return nil
}

func validateReviewedInputs(inputs toolcontract.ReviewedOperationInputs) error {
	if !inputs.Valid() {
		return fmt.Errorf("reviewed inputs require admitted known lossless representation")
	}
	limits, err := receiptLimits()
	if err != nil {
		return err
	}
	if int64(len(inputs.CanonicalArguments())) > limits.bytes {
		return fmt.Errorf("reviewed encoded input exceeds receipt trace ceiling")
	}
	return nil
}
