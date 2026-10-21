// Package commandprogram defines execution coordination and its consumer ports.
// Process supervision settles aggregates; invoked domains settle their effects.
package commandprogram

import (
	"context"
	"io"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Streams carry complete bytes. Display previews are a separate projection.
type Streams struct {
	Input  io.Reader
	Output io.Writer
	Error  io.Writer
}

// OperationRequest contains expanded operands, before per-child admission.
type OperationRequest struct {
	Invocation  toolcontract.OperationInvocation
	OperationID string
	Arguments   []string
	EffectMode  string
}

// OperationDispatcher binds an offered operation and invokes its existing owner.
// Implementations must perform admission per child, not reuse an outer permit.
type OperationDispatcher interface {
	Invoke(context.Context, OperationRequest, Streams) (toolcontract.ProducerResult, error)
}

// CapturedSource is an execution input, not model-observation evidence.
type CapturedSource struct {
	Pin   toolcontract.SourcePin
	Bytes []byte
}

type SourceCapture interface {
	Capture(context.Context, toolcontract.OperationInvocation, string) (CapturedSource, error)
}

// ExternalLauncher must enter the host launch-plan and confinement boundary.
type ExternalLauncher interface {
	Launch(context.Context, OperationRequest, Streams) (toolcontract.ProducerResult, error)
}

// HeldEffects keeps exact prepared inputs; approval revalidates before one attempt.
type HeldEffects interface {
	Resume(context.Context, toolcontract.ExecutionIdentity) (toolcontract.ProducerResult, error)
	Stop(context.Context, toolcontract.ExecutionIdentity) error
}

// ProgressAdmission invokes the existing progress owner. Its outcome is recorded
// independently; successful progress never authorizes a program child effect.
type ProgressAdmission interface {
	AdmitProgress(context.Context, toolcontract.OperationInvocation) (toolcontract.ProducerResult, error)
}

// ProgramSupervision is implemented at the existing process supervision boundary.
// It settles one aggregate and preserves already-settled child owner facts.
type ProgramSupervision interface {
	RunProgram(context.Context, toolcontract.OperationInvocation, func(context.Context) (toolcontract.ProducerResult, error)) (toolcontract.ProducerResult, error)
}

// ResultProjection delegates remaining obligations to workflow/grounding
// authority. The evaluator cannot clear obligations by interpreting stdout.
type ResultProjection interface {
	ProjectResult(context.Context, toolcontract.OperationInvocation, toolcontract.ProducerResult) error
}
