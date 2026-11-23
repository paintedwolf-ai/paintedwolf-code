package invocation

import (
	"context"
	"fmt"
	"log/slog"

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
	prepared, err := prepareNativeStart(bound, modelCallID, inputs)
	if err != nil {
		return err
	}
	start := reviewedOwnerStart{programID: in.Identity.ProgramID, executionID: in.Identity.ExecutionID, record: prepared.record}
	if err := r.Inputs.startReviewedOwner(ctx, r.Recorder, start, inputs); err != nil {
		return err
	}
	r.OwnerStartRecorded = true
	notifyActivity(ctx, in.Identity.ProgramID, in.Identity.ExecutionID)
	return nil
}

// BeforeNativeApprovalWait records a prepared child waiting on its first checkpoint.
func (r *ProgramAdmissionRecorder) BeforeNativeApprovalWait(ctx context.Context, bound toolcontract.BoundOperation, checkpointID string) {
	if r.Recorder == nil {
		return
	}
	if err := r.Recorder.WaitChild(ctx, bound.Invocation().Identity.ExecutionID, checkpointID); err != nil {
		slog.DebugContext(ctx, "command approval wait could not be recorded", "error", err)
	}
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
