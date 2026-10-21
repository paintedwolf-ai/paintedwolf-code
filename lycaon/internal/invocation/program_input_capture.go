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

// ProgramInputCapture binds inspection to actual host program/capture scope and
// shared resource pools. Scope is captured by the introducing host entry, never
// derived from argument text. Resolved credential values are not input to this
// producer. The original opaque review identity stays in the receipt separately.
type ProgramInputCapture struct {
	// State is the actual aggregate program lease, distinct from Outputs session storage.
	State                      toolcontract.ReviewStateAdmission
	Outputs                    *ProgramOutputRetention
	Projector                  *captureprojection.Projector
	ProgramID                  string
	Scope                      captureprojection.Scope
	Screening                  captureprojection.StreamOptions
	MaximumNodes, MaximumDepth int
}

// ProjectOperationInputs is the native approval inspection producer. It screens
// raw keys/string bytes using the actual bounded capture owner; its lease must
// remain held through lossless encoding and card/event publication.
func (p ProgramInputCapture) ProjectOperationInputs(ctx context.Context, state toolcontract.ReviewStateAdmission, arguments map[string]any) (map[string]any, func(), error) {
	projected, _, release, err := p.project(ctx, state, arguments)
	return projected, release, err
}

func (p ProgramInputCapture) project(ctx context.Context, state toolcontract.ReviewStateAdmission, arguments map[string]any) (map[string]any, captureprojection.Metadata, func(), error) {
	if p.Projector == nil || state == nil || p.Scope.RootSessionID == "" || p.Scope.SessionID == "" || p.ProgramID == "" {
		return nil, captureprojection.Metadata{}, nil, fmt.Errorf("native inspection requires actual scoped bounded capture")
	}
	projected, metadata, release, err := p.Projector.Value(ctx, p.Scope, "operation.arguments", arguments, captureprojection.ValueOptions{State: state, Stream: p.Screening, MaximumNodes: p.MaximumNodes, MaximumDepth: p.MaximumDepth})
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

func (p ProgramInputCapture) RetainReviewedProgramInputs(ctx context.Context, in toolcontract.OperationInvocation, review toolcontract.ReviewedOperationInputs) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	if in.Kind != toolcontract.ProgramChildInvocation || in.Identity.ProgramID != p.ProgramID || in.SessionID != p.Scope.SessionID || in.Scope.ProjectID != p.Scope.ProjectID {
		return "", fmt.Errorf("reviewed input capture does not match actual child scope")
	}
	return p.retain(ctx, in.Identity.ExecutionID, review, func(reference string, spill *tooloutput.StreamSpill, redacted bool) (*tooloutput.RetainedStreamBody, error) {
		return p.Outputs.recorder.AttachProgramOutput(ctx, p.ProgramID, RetainedOutput{ExecutionID: in.Identity.ExecutionID, Stream: "arguments", Reference: reference, Complete: true, Redacted: redacted}, spill, p.Outputs.resources)
	})
}

func (p ProgramInputCapture) RetainReviewedProgressInputs(ctx context.Context, in toolcontract.ProgramProgressIdentity, review toolcontract.ReviewedOperationInputs) (string, error) {
	if err := in.Scope.Validate(); err != nil {
		return "", err
	}
	if in.ExecutionID == "" || in.ActorID == "" || in.ProgramID != p.ProgramID || in.SessionID != p.Scope.SessionID || in.RootSessionID != p.Scope.RootSessionID || in.Scope.ProjectID != p.Scope.ProjectID {
		return "", fmt.Errorf("reviewed input capture does not match actual progress scope")
	}
	return p.retain(ctx, in.ExecutionID, review, func(reference string, spill *tooloutput.StreamSpill, redacted bool) (*tooloutput.RetainedStreamBody, error) {
		return p.Outputs.recorder.attachProgressInput(ctx, in, reference, spill, redacted)
	})
}

func (p ProgramInputCapture) retain(ctx context.Context, execution string, review toolcontract.ReviewedOperationInputs, attach func(string, *tooloutput.StreamSpill, bool) (*tooloutput.RetainedStreamBody, error)) (string, error) {
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
	projected, metadata, releaseProjection, err := p.project(ctx, p.State, arguments)
	if err != nil {
		return "", err
	}
	defer releaseProjection()
	screened, releaseEncoding, err := toolcontract.NewReviewedOperationInputs(ctx, p.State, projected)
	if err != nil {
		return "", err
	}
	defer releaseEncoding()
	if err = validateReviewedInputs(screened); err != nil {
		return "", err
	}
	reference := "output:operation-input:" + uuid.NewString()
	limits, err := receiptLimits()
	if err != nil {
		return "", err
	}
	err = p.Outputs.retainInput(ctx, execution, reference, screened.CanonicalArguments(), limits.bytes, metadata.RedactedCount > 0, attach)
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
	// Reserve the ownership slot BEFORE storage begins, including failure paths.
	// No generated spill or ambiguous commit can outlive an uncharged local value.
	release, err := r.resources.ReserveProgramState(ctx, 16384+int64(len(execution)+len(reference))*8)
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

var _ ReviewedProgramInputRetention = ProgramInputCapture{}
var _ ReviewedProgressInputRetention = ProgramInputCapture{}

// PrepareChild atomically attaches the actual screened expanded-input body and
// the new dynamic receipt. The caller supplies no placeholder body reference.
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
	})
}
