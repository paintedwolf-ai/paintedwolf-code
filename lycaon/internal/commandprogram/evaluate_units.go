package commandprogram

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

var _ Substitution = (*evaluation)(nil)

// Substitute is the expander's Substitution port: one reached $(...) unit runs
// as its own child execution in an isolated frame of the frame expanding it,
// with stdout going to the expander's bounded writer. Its ordinary status is
// retained for the containing command; host stops latch the shared program.
// ctx carries the substitution depth and reaches nested expansion unchanged.
func (e *evaluation) Substitute(ctx context.Context, scope ExpansionScope, request SubstitutionRequest, stdout io.Writer) (toolcontract.ProducerResult, error) {
	f, ok := scope.(*frame)
	if !ok || f.e != e {
		return toolcontract.ProducerResult{}, errors.New("substitution scope is not a frame of this evaluation")
	}
	part := request.Unit
	run, err := f.beginRecorded(ctx, part.SyntaxLocation, "$()")
	if err != nil {
		return toolcontract.ProducerResult{}, err
	}
	//nolint:contextcheck // Settlement retains BeginExecution values without cancellation to seal earlier effects.
	defer run.close()
	g, release, err := f.isolate(ctx, nil)
	if err != nil {
		return toolcontract.ProducerResult{}, f.expansionRefusal(part.SyntaxLocation, err)
	}
	defer release()
	g.parent, g.loops, g.returnable = run.inv, 0, false
	g.state.options.errexit = false
	g.streams = Streams{Input: NullInput(), Output: stdout, Error: f.streams.Error}
	_, err = g.statements(ctx, part.Statements)
	result := ordinaryResult(g.state.control.Status())
	if stop, ok := g.state.latch.Stop(); ok {
		result = stop.Result
	}
	//nolint:contextcheck // Settlement retains BeginExecution values without cancellation to seal earlier effects.
	run.settle(result, err)
	if err == nil && f.substs != nil {
		f.substs.record(run.inv.Identity.ExecutionID, result.Status)
	}
	return result, err
}

// source captures and validates the whole reached unit through the source
// owner, then runs it in the current frame. A capture, encoding or syntax
// refusal latches before any sourced statement; earlier parent effects stay.
func (f *frame) source(ctx context.Context, s commandsurface.Statement, fields Fields) error {
	if len(fields) > 1 && fields[1].Value != "--" && strings.HasPrefix(fields[1].Value, "-") {
		return f.builtinStatus(s.SyntaxLocation, "source", 2)
	}
	spec, _, err := ResolveCommand(fields)
	if err != nil {
		return err
	}
	bound, err := f.e.ports.Binder.Bind(spec, fields)
	var argv *ArgvError
	if errors.As(err, &argv) {
		return f.refuse(s.SyntaxLocation, argv.Code, argv.Expected, err)
	}
	if err != nil {
		return err
	}
	operands := bound.Operands()
	if len(operands) != 1 || (!bound.EndOfOptions && strings.HasPrefix(operands[0].Value, "-")) {
		return f.builtinStatus(s.SyntaxLocation, "source", 2)
	}
	if int64(f.sources+1) > f.e.limits["source_depth"] {
		return f.refuseLimit(s.SyntaxLocation, "source_depth", int64(f.sources+1))
	}
	run, err := f.beginRecorded(ctx, s.SyntaxLocation, "source")
	if err != nil {
		return err
	}
	//nolint:contextcheck // Settlement retains BeginExecution values without cancellation to seal earlier effects.
	defer run.close()
	parsed, captured, err := LoadSourceUnit(ctx, f.e.ports.Sources, run.inv, operands[0].Value, f.e.ports.Memory)
	if captured.Pin.SourceID != "" {
		f.e.recordSource(run.inv.Identity.ExecutionID, captured.Pin)
	}
	if err != nil {
		//nolint:contextcheck // Settlement retains BeginExecution values without cancellation to seal earlier effects.
		return f.sourceRefusal(run, s, err)
	}
	lease, err := parsed.SnapshotWithMemory(ctx, f.e.ports.Memory)
	_ = parsed.Close()
	if err != nil {
		//nolint:contextcheck // Settlement retains BeginExecution values without cancellation to seal earlier effects.
		run.settle(ownerResult(err), err)
		return f.stateRefusal(s.SyntaxLocation, err)
	}
	defer func() { _ = lease.Close() }()
	if err := preflight(lease.Plan.Statements, f.e.ports); err != nil {
		//nolint:contextcheck // Settlement retains BeginExecution values without cancellation to seal earlier effects.
		run.settle(toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: "COMMAND_SYNTAX_UNSUPPORTED", EffectCertainty: toolcontract.NoEffect}, err)
		var refusal *EvaluationRefusal
		errors.As(err, &refusal)
		return f.refuse(refusal.Node, refusal.Code, refusal.Expected, nil)
	}
	if registrar, ok := f.e.ports.Executions.(SourceUnitRegistrar); ok {
		if err := registrar.RegisterSourceUnit(ctx, run.inv, captured, lease.Plan); err != nil {
			//nolint:contextcheck // Settlement retains BeginExecution values without cancellation to seal earlier effects.
			run.settle(ownerResult(err), err)
			return err
		}
	}
	g := *f
	g.parent = run.inv
	g.parent.SourceUnitID = captured.Pin.SourceID
	g.unit = captured.Pin.SourceID
	g.sources = f.sources + 1
	g.returnable = true
	sig, err := g.statements(ctx, lease.Plan.Statements)
	result := ordinaryResult(f.state.control.Status())
	if stop, ok := f.state.latch.Stop(); ok {
		result = stop.Result
	}
	//nolint:contextcheck // Settlement retains BeginExecution values without cancellation to seal earlier effects.
	run.settle(result, err)
	if err == nil && sig.kind == signalReturn {
		return nil
	}
	return err
}

func (f *frame) sourceRefusal(run *childRun, s commandsurface.Statement, err error) error {
	var diagnostic *commandsurface.SyntaxDiagnostic
	var integrity *SourceIntegrityError
	switch {
	case errors.As(err, &diagnostic):
		run.settle(toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: diagnostic.Code, EffectCertainty: toolcontract.NoEffect}, err)
		return f.refuse(s.SyntaxLocation, diagnostic.Code, diagnostic.Expected, err)
	case errors.As(err, &integrity):
		run.settle(toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: "COMMAND_OPERAND_INVALID", EffectCertainty: toolcontract.NoEffect}, err)
		return f.refuse(s.SyntaxLocation, "COMMAND_OPERAND_INVALID", "an exact captured source unit", err)
	default:
		return f.settle(run, ownerResult(err), err)
	}
}

func (e *evaluation) recordSource(executionID string, pin toolcontract.SourcePin) {
	e.mu.Lock()
	e.sources = append(e.sources, SourceRecord{ExecutionID: executionID, Pin: pin})
	e.mu.Unlock()
}

// background hands a trailing & statement to host job supervision. The job runs
// in an isolated frame with captured variables and directory, its own status
// and host-stop latch, and the streams the supervisor owns for it. The parent
// continues at once with status 0 and $! naming the session job handle.
func (f *frame) background(ctx context.Context, s commandsurface.Statement) error {
	run, err := f.beginRecorded(ctx, s.SyntaxLocation, "&")
	if err != nil {
		return err
	}
	job, release, err := f.isolate(ctx, &OutcomeLatch{})
	if err != nil {
		//nolint:contextcheck // Settlement retains BeginExecution values without cancellation to seal earlier effects.
		run.close()
		return f.stateRefusal(s.SyntaxLocation, err)
	}
	job.parent, job.loops, job.returnable = run.inv, 0, false
	statement := s
	statement.Background = false
	handle, err := f.e.ports.Supervision.StartJob(ctx, run.inv, func(jobCtx context.Context, streams Streams) (toolcontract.ProducerResult, error) {
		defer release()
		job.streams = streams.withDefaults()
		_, err := job.statement(jobCtx, statement)
		result := ordinaryResult(job.state.control.Status())
		if stop, ok := job.state.latch.Stop(); ok {
			result = stop.Result
		}
		return result, err
	})
	if err != nil {
		release()
		//nolint:contextcheck // Settlement retains BeginExecution values without cancellation to seal earlier effects.
		return f.settle(run, ownerResult(err), err)
	}
	run.record.JobHandle = handle
	f.state.lastJob, f.state.hasJob = handle, true
	f.e.mu.Lock()
	f.e.jobs = append(f.e.jobs, handle)
	f.e.mu.Unlock()
	//nolint:contextcheck // Settlement retains BeginExecution values without cancellation to seal earlier effects.
	return f.settle(run, ordinaryResult(0), nil)
}

func (s Streams) withDefaults() Streams {
	if s.Input == nil {
		s.Input = NullInput()
	}
	if s.Output == nil {
		s.Output = io.Discard
	}
	if s.Error == nil {
		s.Error = io.Discard
	}
	return s
}
