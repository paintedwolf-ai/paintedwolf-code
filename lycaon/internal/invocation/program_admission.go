package invocation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ReviewedProgramInputRetention screens and retains final opaque-reference inputs
// outside the SQL transaction. Resolved secret values never cross this port.
type ReviewedProgramInputRetention interface {
	RetainReviewedProgramInputs(context.Context, toolcontract.OperationInvocation, string) (string, error)
}

// ProgramAdmissionRecorder connects the existing admission start port to durable
// invocation facts. Capture and screening finish before its writer transaction.
type ProgramAdmissionRecorder struct {
	Recorder *SQLRecorder
	Inputs   ReviewedProgramInputRetention
}

func (r ProgramAdmissionRecorder) BeforeNativeOwner(ctx context.Context, bound toolcontract.BoundOperation, modelCallID string, inputs toolcontract.ReviewedOperationInputs) error {
	if r.Recorder == nil || r.Inputs == nil || !validDigest(inputs.ArgumentsDigest) {
		return fmt.Errorf("program start requires screened reviewed-input retention")
	}
	if err := validateReviewedInputs(inputs); err != nil {
		return err
	}
	var err error
	if err = bound.Invocation().Validate(); err != nil {
		return err
	}
	reference, err := r.Inputs.RetainReviewedProgramInputs(ctx, bound.Invocation(), inputs.CanonicalArguments)
	if err != nil {
		return err
	}
	if reference == "" {
		return fmt.Errorf("reviewed input retention returned no reference")
	}
	return r.Recorder.startNativeOwner(ctx, bound, modelCallID, inputs.ArgumentsDigest, reference)
}

func validateReviewedInputs(inputs toolcontract.ReviewedOperationInputs) error {
	// Validate the exact canonical object, not a credential-resolved map. Reject
	// alternate encodings so the digest and inspected input cannot name two objects.
	limits, err := receiptLimits()
	if err != nil {
		return err
	}
	if int64(len(inputs.CanonicalArguments)) > limits.arguments {
		return fmt.Errorf("reviewed input exceeds argument byte ceiling")
	}
	var object map[string]any
	decoder := json.NewDecoder(strings.NewReader(inputs.CanonicalArguments))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return fmt.Errorf("reviewed arguments require a canonical object")
	}
	canonical, err := authzcontext.CanonicalJSON(object)
	if err != nil {
		return err
	}
	if string(canonical) != inputs.CanonicalArguments {
		return fmt.Errorf("reviewed arguments are not canonical")
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != inputs.ArgumentsDigest {
		return fmt.Errorf("reviewed argument digest mismatch")
	}

	return nil
}
