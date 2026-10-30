package invocation

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// ProgramInputCapture screens inspection bytes within the host capture scope.
type ProgramInputCapture struct {
	// State charges the program; Outputs charges the session retention lifetime.
	State                      toolcontract.ReviewStateAdmission
	Outputs                    *ProgramOutputRetention
	Projector                  *captureprojection.Projector
	ProgramID                  string
	Scope                      captureprojection.Scope
	Screening                  captureprojection.StreamOptions
	MaximumNodes, MaximumDepth int
}

// ProjectOperationInputs retains its lease through encoding and publication.
func (p ProgramInputCapture) ProjectOperationInputs(ctx context.Context, state toolcontract.ReviewStateAdmission, arguments map[string]any) (map[string]any, func(), error) {
	projected, _, release, err := p.project(ctx, state, "operation.arguments", arguments)
	return projected, release, err
}

// ProjectOperationDiagnostic screens owner diagnostics before model feedback.
func (p ProgramInputCapture) ProjectOperationDiagnostic(ctx context.Context, state toolcontract.ReviewStateAdmission, data map[string]any) (map[string]any, func(), error) {
	projected, _, release, err := p.project(ctx, state, "operation.diagnostic", data)
	return projected, release, err
}

func (p ProgramInputCapture) project(ctx context.Context, state toolcontract.ReviewStateAdmission, label string, arguments map[string]any) (map[string]any, captureprojection.Metadata, func(), error) {
	if p.Projector == nil || state == nil || p.Scope.RootSessionID == "" || p.Scope.SessionID == "" || p.ProgramID == "" {
		return nil, captureprojection.Metadata{}, nil, fmt.Errorf("native inspection requires actual scoped bounded capture")
	}
	projected, metadata, release, err := p.Projector.Value(ctx, p.Scope, label, arguments, captureprojection.ValueOptions{State: state, Stream: p.Screening, MaximumNodes: p.MaximumNodes, MaximumDepth: p.MaximumDepth})
	if err != nil {
		return nil, captureprojection.Metadata{}, nil, err
	}
	result, ok := projected.(map[string]any)
	if !ok {
		release()
		return nil, captureprojection.Metadata{}, nil, fmt.Errorf("screened native inspection is not an object")
	}
	return result, metadata, release, nil
}

func (p ProgramInputCapture) validateNativeScope(in toolcontract.OperationInvocation) error {
	if err := in.Validate(); err != nil {
		return err
	}
	if in.Kind != toolcontract.ProgramChildInvocation || in.Identity.ProgramID != p.ProgramID || in.SessionID != p.Scope.SessionID || in.Scope.ProjectID != p.Scope.ProjectID {
		return fmt.Errorf("reviewed input capture does not match actual child scope")
	}
	return nil
}

func (p ProgramInputCapture) retain(ctx context.Context, execution string, review toolcontract.ReviewedOperationInputs, attach func(string, *tooloutput.StreamSpill, bool) (*tooloutput.RetainedStreamBody, error), inline func(string, []byte, bool) error) (string, error) {
	if p.Outputs == nil || p.Outputs.resources == nil || p.State == nil || p.Scope.SessionID != p.Outputs.sessionID || ctx == nil {
		return "", fmt.Errorf("reviewed input capture requires invocation output lifetime")
	}
	if err := validateReviewedInputs(review); err != nil {
		return "", err
	}
	arguments, releaseArguments, err := review.Arguments(ctx, p.State)
	if err != nil {
		return "", err
	}
	defer releaseArguments()
	projected, metadata, releaseProjection, err := p.project(ctx, p.State, "operation.arguments", arguments)
	if err != nil {
		return "", err
	}
	defer releaseProjection()
	screened, releaseEncoding, err := toolcontract.NewReviewedOperationInputs(ctx, p.State, projected)
	if err != nil {
		return "", err
	}
	defer releaseEncoding()
	if err := validateReviewedInputs(screened); err != nil {
		return "", err
	}
	reference := "output:operation-input:" + uuid.NewString()
	canonical := screened.CanonicalArguments()
	if inline != nil && len(canonical) <= maxInlineProgramInput {
		if err := inline(reference, []byte(canonical), metadata.RedactedCount > 0); err != nil {
			return "", err
		}
		return reference, nil
	}
	// Bound each screened body independently of the program trace.
	const maximumInputBodyBytes = 8 << 20
	err = p.Outputs.retainInput(ctx, execution, reference, canonical, maximumInputBodyBytes, metadata.RedactedCount > 0, attach)
	if err != nil {
		return "", err
	}
	return reference, nil
}

func (r *ProgramOutputRetention) retainInput(ctx context.Context, execution, reference, canonical string, maximum int64, redacted bool, attach func(string, *tooloutput.StreamSpill, bool) (*tooloutput.RetainedStreamBody, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready || r.release == nil {
		return fmt.Errorf("program output recovery is incomplete")
	}
	// Charge ownership before storage; failed commits retain the body and leases.
	release, err := r.resources.ReserveProgramState(ctx, 512+int64(len(execution)+len(reference))*2)
	if err != nil {
		return err
	}
	if release == nil {
		return fmt.Errorf("input output-lifetime admission returned no lease")
	}
	spill, err := tooloutput.RetainScreenedStream(ctx, tooloutput.ScreenedReader(io.NopCloser(strings.NewReader(canonical))), tooloutput.StreamSpillOptions{Store: r.store, ClaimID: execution, MaximumBytes: maximum, Admission: r.resources, WorkingMemory: r.resources, RetainedState: r.resources})
	if spill == nil {
		release()
		return err
	}
	entry := r.ownSpill(spill, release)
	if err != nil {
		return err
	}
	if !spill.Complete {
		return fmt.Errorf("screened operation input retention is incomplete: %w", spill.Failure)
	}
	body, err := attach(reference, spill, redacted)
	if err != nil {
		return err
	}
	entry.pending = nil
	entry.body = body
	return nil
}

// PrepareChild commits screened expanded inputs with the dynamic receipt.
func (p ProgramInputCapture) PrepareChild(ctx context.Context, in toolcontract.ProgramChildStart) (string, error) {
	invocation := in.Invocation
	if err := invocation.Validate(); err != nil {
		return "", err
	}
	if in.ArgumentsReference != "" || invocation.Kind != toolcontract.ProgramChildInvocation || invocation.Identity.ProgramID != p.ProgramID || invocation.SessionID != p.Scope.SessionID || invocation.Scope.ProjectID != p.Scope.ProjectID {
		return "", fmt.Errorf("expanded child capture requires actual invocation scope and generated reference")
	}
	return p.retain(ctx, invocation.Identity.ExecutionID, in.ExpandedInputs, func(reference string, spill *tooloutput.StreamSpill, redacted bool) (*tooloutput.RetainedStreamBody, error) {
		in.ArgumentsReference = reference
		return p.Outputs.recorder.prepareChildBody(ctx, in, spill, redacted)
	}, func(reference string, body []byte, redacted bool) error {
		in.ArgumentsReference = reference
		return p.Outputs.prepareChildInline(ctx, in, body, redacted)
	})
}
