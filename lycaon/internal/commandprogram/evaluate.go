package commandprogram

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// EvaluationPorts are the owners one program evaluation reaches. Evaluate
// refuses before the first statement when the plan contains a construct whose
// port is absent; it never substitutes a shell or another owner.
type EvaluationPorts struct {
	// Invocation is the program's root execution; every child derives from it.
	Invocation toolcontract.OperationInvocation
	Dispatch   Dispatch
	Binder     ArgvBinder
	// Files is the logical-path view pathname expansion lists; Evaluate builds
	// the program's WordExpander over it with itself as the Substitution port.
	Files       commandsurface.GlobFiles
	Publication OutputPublication
	Input       InputRedirection
	Sources     SourceCapture
	Supervision ProgramSupervision
	// Predicates supplies the filesystem and pattern ports for test, [ and [[.
	// Expansion, scope and memory are bound per reached frame.
	Predicates PredicateRuntime
	Memory     *toolcontract.ProgramMemory
	// Executions records the executions the evaluator runs itself; dispatched
	// operations are recorded by their owners. Nil records none.
	Executions ExecutionRecorder
	Streams    Streams
	// Environment is the captured child environment; its entries start exported.
	Environment []string
}

// ProgramOutcome is the evaluated program's ordinary status plus the first host
// stop and the bounded trace of every executed child. It never rewrites a
// child owner's result; settlement and receipts stay with their owners.
type ProgramOutcome struct {
	Status   int
	Exited   bool
	Stop     *HostStop
	Children []ChildRecord
	Sources  []SourceRecord
	Jobs     []string
}

// ChildRecord is one dynamic execution: a dispatched operation, an in-process
// builtin, a function call, a substitution, a redirection or a source unit.
// Diagnostic keeps the owner's error (including its rejection code) verbatim.
type ChildRecord struct {
	Identity     toolcontract.ExecutionIdentity
	SourceUnitID string
	Span         toolcontract.SourceSpan
	Command      string
	Executed     bool
	Result       toolcontract.ProducerResult
	Diagnostic   error
	// JobHandle is the session job a trailing & statement started.
	JobHandle string
}

// SourceRecord binds a reached source statement to its exact captured bytes.
type SourceRecord struct {
	ExecutionID string
	Pin         toolcontract.SourcePin
}

// Settlement projects the outcome onto the receipt owner's aggregate record.
func (o ProgramOutcome) Settlement(detailReference string) toolcontract.ProgramSettlement {
	status := o.Status
	settlement := toolcontract.ProgramSettlement{State: "completed", Status: &status, DetailReference: detailReference}
	if o.Stop != nil {
		settlement.StopCode = o.Stop.Result.StopCode
		switch o.Stop.Kind {
		case StopAuthority:
			settlement.State = "rejected"
		case StopInterrupted:
			settlement.State = "interrupted"
		default:
			settlement.State = "failed"
		}
	}
	return settlement
}

// EvaluationRefusal is a typed host refusal at a reached or statically present
// node. Effects settled before it remain recorded; nothing after it is admitted.
type EvaluationRefusal struct {
	Code     string
	Node     commandsurface.SyntaxLocation
	Expected string
	Cause    error
	// Command, Option and Form name a refused native form: the command, the
	// refused word and the command's supported form.
	Command, Option, Form string
}

func (e *EvaluationRefusal) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s at %d:%d: %s: %v", e.Code, e.Node.Span.Line, e.Node.Span.Column, e.Expected, e.Cause)
	}
	return fmt.Sprintf("%s at %d:%d: %s", e.Code, e.Node.Span.Line, e.Node.Span.Column, e.Expected)
}
func (e *EvaluationRefusal) Unwrap() error { return e.Cause }

// MachineErrorCode exposes the public rejection code for operation feedback.
func (e *EvaluationRefusal) MachineErrorCode() string { return e.Code }

// Evaluate walks one validated plan under the host statement scheduler. All
// effects go through ports.Dispatch; status, host stops and resource ceilings
// follow the frozen catalog. Background jobs it starts keep running under
// ports.Supervision after it returns; no interpreter state survives otherwise.
func Evaluate(ctx context.Context, plan commandsurface.ProgramSnapshot, ports EvaluationPorts) (ProgramOutcome, error) {
	if err := ports.validate(plan); err != nil {
		return ProgramOutcome{}, err
	}
	limits, err := catalogEvaluationLimits()
	if err != nil {
		return ProgramOutcome{}, err
	}
	if err := preflight(plan.Statements, ports); err != nil {
		return ProgramOutcome{}, err
	}
	e := &evaluation{ports: ports, limits: limits, programName: plan.SourceName}
	e.expander, err = NewWordExpander(ExpanderPorts{Substitutions: e, Files: ports.Files, Memory: ports.Memory})
	if err != nil {
		return ProgramOutcome{}, err
	}
	root, release, err := e.rootFrame(ctx)
	if err != nil {
		return ProgramOutcome{}, err
	}
	defer release()
	_, err = root.statements(ctx, plan.Statements)
	return e.outcome(root), err
}

func (p EvaluationPorts) validate(plan commandsurface.ProgramSnapshot) error {
	if p.Dispatch == nil || p.Binder == nil || p.Files == nil || p.Memory == nil {
		return fmt.Errorf("evaluation requires dispatch, argv binding, a glob view and program memory")
	}
	if err := p.Invocation.Validate(); err != nil {
		return err
	}
	if p.Invocation.Kind != toolcontract.ProgramChildInvocation {
		return fmt.Errorf("evaluation requires a program execution invocation")
	}
	if plan.Version != toolcontract.LanguageVersion || plan.CatalogDigest != p.Invocation.CatalogDigest {
		return fmt.Errorf("plan does not match the invocation's frozen language catalog")
	}
	return nil
}
