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
// Environment is the frame's exported NAME=value list, including assignments
// scoped to this one dispatch, sorted by name; it carries no ambient host state.
type OperationRequest struct {
	Invocation toolcontract.OperationInvocation
	// OperationID is the catalog command name ("external" for launches); the
	// typed owner's operation identity is the owner's own fact.
	OperationID string
	Statement   commandsurface.Statement
	Argv        Fields
	Environment []string
	EffectMode  string
	// Gap is set when a native name's form has no native owner and the
	// launch would run the installed program; dispatch decides whether it may.
	Gap *toolcontract.NativeGap
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
// Variables is the invocation-local implementation.
type ExpansionScope interface {
	Variable(name string) (value string, set bool)
	// ProgramName is $0, the program or sourced unit identity.
	ProgramName() string
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
	// ExpandDocument yields a here-document body: literal when its delimiter
	// is quoted, otherwise parameters and substitutions without splitting.
	ExpandDocument(context.Context, ExpansionScope, commandsurface.ProgramRedirect) (string, error)
}

// SubstitutionRequest is one reached $() or backtick unit. Depth counts the
// enclosing substitutions, starting at one for an outermost unit.
type SubstitutionRequest struct {
	Unit  commandsurface.SubstitutionPart
	Depth int
}

// Substitution evaluates a reached unit through the evaluator's own admission,
// dispatch and receipts, as a distinct child execution in an isolated frame.
// Stdout receives the unit's complete output and refuses bytes past the
// substitution ceiling. The implementation retains each ordinary status (the
// last one is $? for an assignment-only command) and latches host stops; ctx
// carries the substitution depth into nested expansion and must be passed on.
type Substitution interface {
	Substitute(ctx context.Context, scope ExpansionScope, request SubstitutionRequest, stdout io.Writer) (toolcontract.ProducerResult, error)
}

// NestedCommand runs a command operand of a native utility (env's COMMAND)
// as its own child execution of the parent request, with exactly the given
// environment. The implementer allocates the child identity and receipt and
// dispatches through the same admission as any reached command.
type NestedCommand interface {
	RunNested(ctx context.Context, parent OperationRequest, argv Fields, environment map[string]string, streams Streams) (toolcontract.ProducerResult, error)
}

// ExecutionRecorder records an execution the evaluator runs itself before any
// descendant is dispatched, and settles it with its record once.
type ExecutionRecorder interface {
	BeginExecution(context.Context, toolcontract.OperationInvocation, string) error
	SettleExecution(context.Context, toolcontract.OperationInvocation, ChildRecord) error
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
// LoadSourceUnit bytes require a live parsed-program or execution-snapshot lease.
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

// SourceUnitRegistrar records a reached sourced unit's pin, bytes and static
// nodes before its statements run so child executions validate against the
// retained program.
type SourceUnitRegistrar interface {
	RegisterSourceUnit(context.Context, toolcontract.OperationInvocation, CapturedSource, commandsurface.ProgramSnapshot) error
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
// StartJob supervises a statement-trailing & job: run receives the job's own
// lifetime context and the bounded output streams the supervisor owns for it;
// cwd and environment are already captured. The returned handle is the
// session-scoped job identity that $! and wait/kill address.
type ProgramSupervision interface {
	RunProgram(context.Context, toolcontract.OperationInvocation, func(context.Context) (toolcontract.ProducerResult, error)) (toolcontract.ProducerResult, error)
	StartJob(context.Context, toolcontract.OperationInvocation, func(context.Context, Streams) (toolcontract.ProducerResult, error)) (string, error)
}

// Dispatch reaches operation owners and confined launch from one directory
// frame. Fork captures the frame's current directory for an isolated frame
// (parentheses, substitutions, pipeline stages, background jobs); cd inside the
// fork never reaches the parent. Release returns the fork's capture.
type Dispatch interface {
	OperationDispatcher
	ExternalLauncher
	Fork(context.Context) (Dispatch, func(), error)
}

// NativeGaps is implemented by a Dispatch that can run a native name's
// unsupported form as the installed program, recorded as a native gap.
type NativeGaps interface {
	GapLaunchable(OperationRequest) bool
}

// FramePredicates is implemented by a Dispatch frame whose file predicates
// resolve operands from that frame's current directory.
type FramePredicates interface {
	PredicateFiles() PredicateFiles
}

// FrameRedirections is implemented by a Dispatch frame whose redirect owners
// resolve targets from that frame's current directory.
type FrameRedirections interface {
	Redirections() (InputRedirection, OutputPublication)
}

// InputDestination is one reached `<` redirection after target expansion.
type InputDestination struct {
	Invocation toolcontract.OperationInvocation
	Redirect   commandsurface.ProgramRedirect
	Target     Field
}

// InputRedirection opens a `<` operand through the read owner. The returned
// stream is stdin for one statement; closing it is a consumer stop.
type InputRedirection interface {
	OpenInput(context.Context, InputDestination) (io.ReadCloser, error)
}

// ResultProjection delegates remaining obligations to workflow/grounding
// authority. The evaluator cannot clear obligations by interpreting stdout.
type ResultProjection interface {
	ProjectResult(context.Context, toolcontract.OperationInvocation, toolcontract.ProducerResult) error
}
