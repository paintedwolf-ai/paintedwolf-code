package commandprogram

import (
	"context"
	"errors"
	"fmt"
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
	defer run.close(ctx)
	g, release, err := f.isolate(ctx, nil)
	if err != nil {
		return toolcontract.ProducerResult{}, f.expansionRefusal(part.SyntaxLocation, err)
	}
	defer release()
	g.parent, g.loops, g.returnable = run.inv, 0, false
	g.state.options.errexit = false
	g.io.streams = f.io.streams
	g.io.setReader(0, NullInput())
	g.io.setWriter(1, stdout)
	g.io.setWriter(2, f.io.streams.Error)
	_, err = g.statements(ctx, part.Statements)
	if trapErr := runExitTrap(ctx, g, err); trapErr != nil && err == nil {
		err = trapErr
	}
	result := ordinaryResult(g.state.control.Status())
	if stop, ok := g.state.latch.Stop(); ok {
		result = stop.Result
	}
	run.settle(ctx, result, err)
	if err == nil && f.substs != nil {
		f.substs.record(run.inv.Identity.ExecutionID, result.Status)
	}
	return result, err
}

// source captures and validates the whole reached unit through the source
// owner, then runs it in the current frame. A capture, encoding or syntax
// refusal latches before any sourced statement; earlier parent effects stay.
func (f *frame) source(ctx context.Context, s commandsurface.Statement, fields Fields) (finalErr error) {
	if _, _, err := ResolveCommand(fields); err != nil {
		return err
	}
	operands := fields[1:]
	endOfOptions := false
	if len(operands) > 0 && operands[0].Value == "--" {
		endOfOptions = true
		operands = operands[1:]
	}
	if len(operands) != 1 || (!endOfOptions && strings.HasPrefix(operands[0].Value, "-")) {
		return f.builtinStatus(s.SyntaxLocation, "source", 2)
	}
	if int64(f.sources+1) > f.e.limits["source_depth"] {
		return f.refuseLimit(s.SyntaxLocation, "source_depth", int64(f.sources+1))
	}
	run, err := f.beginRecorded(ctx, s.SyntaxLocation, "source")
	if err != nil {
		return err
	}
	defer run.close(ctx)
	sources := f.e.ports.Sources
	if inputs, _ := f.redirections(); inputs != nil {
		if sc, ok := inputs.(SourceCapture); ok {
			sources = sc
		}
	}
	parsed, captured, err := LoadSourceUnit(ctx, sources, run.inv, operands[0].Value, f.e.ports.Memory)
	if captured.Pin.SourceID != "" {
		f.e.recordSource(run.inv.Identity.ExecutionID, captured.Pin)
	}
	if err != nil {
		return f.sourceRefusal(ctx, run, s, "source", err)
	}
	lease, err := parsed.SnapshotWithMemory(ctx, f.e.ports.Memory)
	_ = parsed.Close()
	if err != nil {
		run.settle(ctx, ownerResult(err), err)
		return f.stateRefusal(s.SyntaxLocation, err)
	}
	defer func() { finalErr = errors.Join(finalErr, lease.Close()) }()
	if err := preflight(lease.Plan.Statements, f.e.ports); err != nil {
		run.settle(ctx, toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: "COMMAND_SYNTAX_UNSUPPORTED", EffectCertainty: toolcontract.NoEffect}, err)
		var refusal *EvaluationRefusal
		errors.As(err, &refusal)
		return f.refuse(refusal.Node, refusal.Code, refusal.Expected, nil)
	}
	if registrar, ok := f.e.ports.Executions.(SourceUnitRegistrar); ok {
		if err := registrar.RegisterSourceUnit(ctx, run.inv, captured, lease.Plan); err != nil {
			run.settle(ctx, ownerResult(err), err)
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
	run.settle(ctx, result, err)
	if err == nil && sig.kind == signalReturn {
		return nil
	}
	return err
}

func (f *frame) sourceRefusal(ctx context.Context, run *childRun, s commandsurface.Statement, command string, err error) error {
	var diagnostic *commandsurface.SyntaxDiagnostic
	var integrity *SourceIntegrityError
	switch {
	case errors.As(err, &diagnostic):
		run.settle(ctx, toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: diagnostic.Code, EffectCertainty: toolcontract.NoEffect}, err)
		f.hostRefusal(command, fmt.Sprintf("line %d", diagnostic.Span.Line), diagnostic.Expected, diagnostic.Code)
		return f.refuse(s.SyntaxLocation, diagnostic.Code, diagnostic.Expected, err)
	case errors.As(err, &integrity):
		run.settle(ctx, toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: "COMMAND_OPERAND_INVALID", EffectCertainty: toolcontract.NoEffect}, err)
		return f.refuse(s.SyntaxLocation, "COMMAND_OPERAND_INVALID", "an exact captured source unit", err)
	default:
		return f.settle(ctx, run, ownerResult(err), err)
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
		run.close(ctx)
		return f.stateRefusal(s.SyntaxLocation, err)
	}
	job.wait, _ = f.wait.Fork()
	job.io.wait = job.wait
	job.parent, job.loops, job.returnable = run.inv, 0, false
	statement := s
	statement.Background = false
	releaseDescriptors := job.io.retain()
	handle, err := f.e.ports.Supervision.StartJob(ctx, run.inv, func(jobCtx context.Context, handle string, streams Streams) (toolcontract.ProducerResult, error) {
		defer release()
		defer job.wait.finish()
		job.wait.Job(handle)
		job.io.streams = streams.withDefaults()
		_, err := job.statement(jobCtx, statement)
		if trapErr := runExitTrap(jobCtx, job, err); trapErr != nil && err == nil {
			err = trapErr
		}
		result := ordinaryResult(job.state.control.Status())
		if stop, ok := job.state.latch.Stop(); ok {
			result = stop.Result
		}
		eligible := err == nil && job.publish.eligible()
		err = errors.Join(err, releaseDescriptors(jobCtx, eligible))
		return result, err
	})
	if err != nil {
		_ = releaseDescriptors(ctx, false)
		job.wait.finish()
		release()
		return f.settle(ctx, run, ownerResult(err), err)
	}
	run.record.JobHandle = handle
	f.state.lastJob, f.state.hasJob = handle, true
	f.e.mu.Lock()
	f.e.jobs = append(f.e.jobs, handle)
	f.e.mu.Unlock()
	return f.settle(ctx, run, ordinaryResult(0), nil)
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
	s.readers[0] = s.Input
	s.writers[1] = s.Output
	s.writers[2] = s.Error
	return s
}
