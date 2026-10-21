package invocation

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ReviewedProgramInputRetention screens and retains final opaque-reference inputs
// outside the SQL transaction. Resolved secret values never cross this port.
type ReviewedProgramInputRetention interface {
	RetainReviewedProgramInputs(context.Context, toolcontract.OperationInvocation, toolcontract.ReviewedOperationInputs) (string, error)
}

// ProgramAdmissionRecorder connects the existing admission start port to durable
// invocation facts. Capture and screening finish before its writer transaction.
type ProgramAdmissionRecorder struct {
	Recorder *SQLRecorder
	Inputs   ReviewedProgramInputRetention
}

func (r ProgramAdmissionRecorder) BeforeNativeOwner(ctx context.Context, bound toolcontract.BoundOperation, modelCallID string, inputs toolcontract.ReviewedOperationInputs) error {
	if r.Recorder == nil || r.Inputs == nil || !validDigest(inputs.ArgumentsDigest()) {
		return fmt.Errorf("program start requires screened reviewed-input retention")
	}
	if err := validateReviewedInputs(inputs); err != nil {
		return err
	}
	var err error
	if err = bound.Invocation().Validate(); err != nil {
		return err
	}
	reference, err := r.Inputs.RetainReviewedProgramInputs(ctx, bound.Invocation(), inputs)
	if err != nil {
		return err
	}
	if reference == "" {
		return fmt.Errorf("reviewed input retention returned no reference")
	}
	return r.Recorder.startNativeOwner(ctx, bound, modelCallID, inputs.ArgumentsDigest(), reference, inputs.Representation())
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
