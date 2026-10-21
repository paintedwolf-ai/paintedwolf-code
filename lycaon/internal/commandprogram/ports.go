// Package commandprogram defines execution coordination and its consumer ports.
// Process supervision settles aggregates; invoked domains settle their effects.
package commandprogram

import (
	"context"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Streams carry complete bytes (BytePipe consumer end, StreamInput, ScreenedPipe
// or the typed null endpoints). Closing Input is a deliberate consumer stop,
// never producer failure. Display previews are a separate projection.
type Streams struct {
	Input  io.ReadCloser
	Output io.Writer
	Error  io.Writer
}

// OperationRequest is one reached simple command after expansion and before
// per-child admission. Statement is the typed plan node; its span is the
// invocation span. Argv is the complete expanded argv, command words included.
type OperationRequest struct {
	Invocation  toolcontract.OperationInvocation
	OperationID string
	Statement   commandsurface.Statement
	Argv        Fields
	EffectMode  string
}

// Validate binds the plan node to the child invocation it is dispatched as.
func (r OperationRequest) Validate() error {
	if r.OperationID == "" || len(r.Argv) == 0 {
		return fmt.Errorf("operation request requires an operation and argv")
	}
	if r.Statement.Span != r.Invocation.Span || string(r.Statement.ID) != r.Invocation.Identity.NodeID {
		return fmt.Errorf("operation request node differs from its child invocation")
	}
	return nil
}

// OperationDispatcher binds an offered operation and invokes its existing owner.
// Implementations must perform admission per child, not reuse an outer permit.
type OperationDispatcher interface {
	Invoke(context.Context, OperationRequest, Streams) (toolcontract.ProducerResult, error)
}

// ExpansionScope is the evaluator-visible state expansion may read. The
// evaluator owns and mutates it; expansion never assigns or records status.
type ExpansionScope interface {
	Variable(name string) (value string, set bool)
	Positional() []string
	LastStatus() int
	LastBackground() (job string, set bool)
}

// Expander expands one reached word under the scope. Substitutions dispatch
// through the evaluator; expanded data is never reparsed as program text.
type Expander interface {
	// ExpandFields applies parameters, substitutions, field splitting and pathname expansion.
	ExpandFields(context.Context, ExpansionScope, commandsurface.DeferredWord) (Fields, error)
	// ExpandScalar yields one value without splitting or pathname expansion.
	ExpandScalar(context.Context, ExpansionScope, commandsurface.DeferredWord) (string, error)
	// ExpandPattern keeps quoted fragments literal for glob and ERE matching.
	ExpandPattern(context.Context, ExpansionScope, commandsurface.DeferredWord) (PredicateValue, error)
}

// ProgramRecorder is the receipt owner's port (invocation.SQLRecorder): one
// parent program, a prepared child per dynamic execution, once-only settlements.
type ProgramRecorder interface {
	BeginProgram(context.Context, toolcontract.ProgramStart) error
	PrepareChild(context.Context, toolcontract.ProgramChildStart) error
	SettleChild(context.Context, string, toolcontract.ProgramChildSettlement) error
	SettleProgram(context.Context, string, toolcontract.ProgramSettlement) error
}

// OutputDestination is one reached output redirection after target expansion.
type OutputDestination struct {
	Invocation toolcontract.OperationInvocation
	Redirect   commandsurface.ProgramRedirect
	Target     Field
}

// OutputPublication opens a redirect destination through its owner. Bytes are
// staged and stay invisible until Publish; nothing is published implicitly.
type OutputPublication interface {
	Open(context.Context, OutputDestination) (StagedOutput, error)
}

// StagedOutput receives a stage's bytes. Publish rechecks the owner's revision
// and authority; Abandon discards staged bytes. Exactly one of them settles it.
type StagedOutput interface {
	io.Writer
	Publish(context.Context) (OutputPublished, error)
	Abandon(context.Context) error
}

// OutputPublished carries the owner's accepted and published revisions.
type OutputPublished struct {
	AcceptedRevision  string
	PublishedRevision string
}

// CapturedSource is an execution input, not model-observation evidence.
type CapturedSource struct {
	Pin   toolcontract.SourcePin
	Bytes []byte
}

// SourceCaptureRequest binds source representation and a pre-materialization ceiling.
// The source implementation must enforce MaximumBytes before reading the full unit.
type SourceCaptureRequest struct {
	Path           string
	MaximumBytes   int64
	Representation toolcontract.SourceRepresentation
}

type SourceCapture interface {
	Capture(context.Context, toolcontract.OperationInvocation, SourceCaptureRequest) (CapturedSource, error)
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
