package commandprogram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/lycaon/lycaon/internal/commandsurface"
)

const procSubstPrefix = "/dev/fd/"

type procSubstPipeWriter struct {
	pipe *BytePipe
	wait *ExecutionWait
}

func (w procSubstPipeWriter) Write(p []byte) (int, error) {
	return w.pipe.write(p, w.wait)
}

func (w procSubstPipeWriter) WithActivity(activity *ExecutionWait) io.Writer {
	w.wait = activity
	return w
}

func (w procSubstPipeWriter) Close() error {
	return w.pipe.CloseProducer(nil)
}

type procSubstStagedOutput struct {
	mu     sync.Mutex
	writer io.WriteCloser
	closed bool
}

// NewProcessSubstitutionStagedOutput adapts a process-substitution writer
// to the StagedOutput interface.
func NewProcessSubstitutionStagedOutput(writer io.WriteCloser) StagedOutput {
	return &procSubstStagedOutput{writer: writer}
}

func (o *procSubstStagedOutput) Write(p []byte) (int, error) {
	return o.writer.Write(p)
}

func (o *procSubstStagedOutput) WithActivity(activity *ExecutionWait) io.Writer {
	return (Streams{Output: o.writer}).WithActivity(activity).Output
}

func (o *procSubstStagedOutput) closeOnce() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	o.mu.Unlock()
	return o.writer.Close()
}

func (o *procSubstStagedOutput) Publish(context.Context) (OutputPublished, error) {
	return OutputPublished{}, o.closeOnce()
}

func (o *procSubstStagedOutput) Abandon(context.Context) error {
	return o.closeOnce()
}

type procSubstEntry struct {
	part         commandsurface.SubstitutionPart
	fd           int
	path         string
	pipe         *BytePipe
	run          *childRun
	frame        *frame
	cancel       context.CancelFunc
	done         chan struct{}
	err          error
	completionMu sync.Mutex
	unblock      func()
}

type procSubstScope struct {
	parent  *procSubstScope
	mu      sync.Mutex
	entries []*procSubstEntry
}

func newProcSubstScope(parent *procSubstScope) *procSubstScope {
	return &procSubstScope{parent: parent}
}

func (s *procSubstScope) add(entry *procSubstEntry) {
	s.mu.Lock()
	s.entries = append(s.entries, entry)
	s.mu.Unlock()
}

// ProcessSubstitute evaluates a reached <(list) or >(list) unit concurrently
// in an isolated frame connected by a bounded BytePipe, returning the typed
// ProcessSubstitution handle bound into the expanded Field.
func (e *evaluation) ProcessSubstitute(ctx context.Context, scope ExpansionScope, request SubstitutionRequest) (*ProcessSubstitution, error) {
	f, ok := scope.(*frame)
	if !ok || f.e != e {
		return nil, errors.New("process substitution scope is not a frame of this evaluation")
	}
	part := request.Unit
	op := part.Operator
	if op != "<(" && op != ">(" {
		op = "<("
	}
	run, err := f.beginRecorded(ctx, part.SyntaxLocation, op)
	if err != nil {
		return nil, err
	}
	run.producer = false
	subCtx, cancel := context.WithCancel(ctx)
	budget, err := NewByteBudget("stream_bytes", e.limits["stream_bytes"])
	activity, _ := f.wait.Fork()
	var pipe *BytePipe
	if err == nil {
		pipe, err = NewBytePipe(subCtx, budget)
	}
	if err != nil {
		cancel()
		activity.finish()
		run.close(ctx)
		return nil, err
	}
	g, release, err := f.isolate(subCtx, nil)
	if err != nil {
		cancel()
		activity.finish()
		_ = pipe.CloseConsumer()
		run.close(ctx)
		return nil, f.expansionRefusal(part.SyntaxLocation, err)
	}
	g.wait = activity
	g.io.wait = activity
	fd, path := e.allocateProcSubstFD()
	g.parent, g.loops, g.returnable = run.inv, 0, false
	g.state.options.errexit = false
	g.io.streams = f.io.streams
	g.io.setWriter(2, &syncWriter{w: f.io.streams.Error})
	var subst *ProcessSubstitution
	if op == "<(" {
		g.producerPipe = pipe
		g.io.setReader(0, NullInput())
		g.io.setWriter(1, pipe)
		subst = NewProcessSubstitution(path, op, func() (io.ReadCloser, bool) {
			return pipeConsumer{pipe: pipe, wait: f.wait}, true
		}, nil)
	} else {
		g.io.setReader(0, pipe.ConsumerEnd())
		g.io.setWriter(1, &syncWriter{w: f.io.streams.Output})
		subst = NewProcessSubstitution(path, op, nil, func() (io.WriteCloser, bool) {
			return procSubstPipeWriter{pipe: pipe}, true
		})
	}
	entry := &procSubstEntry{
		part:   part,
		fd:     fd,
		path:   path,
		pipe:   pipe,
		run:    run,
		frame:  g,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	if f.procSubst != nil {
		f.procSubst.add(entry)
	}
	go func() {
		defer g.wait.finish()
		defer entry.complete()
		defer release()
		defer cancel()
		_, runErr := g.statements(subCtx, part.Statements)
		if op == "<(" && errors.Is(runErr, ErrConsumerClosed) && pipe.Completion().Termination == StreamConsumerClosed {
			runErr = nil
		}
		if trapErr := runExitTrap(subCtx, g, runErr); trapErr != nil && runErr == nil {
			runErr = trapErr
		}
		var streamLimit *StreamLimitError
		if errors.As(runErr, &streamLimit) {
			runErr = g.refuseLimit(part.SyntaxLocation, streamLimit.Limit, streamLimit.Observed)
		}
		if op == "<(" {
			var incomplete error
			if runErr != nil {
				incomplete = errStageIncomplete
			} else if _, stopped := g.state.latch.Stop(); stopped {
				incomplete = errStageIncomplete
			}
			_ = pipe.CloseProducer(incomplete)
		} else {
			_ = pipe.CloseConsumer()
		}
		entry.err = runErr
	}()
	if f.procSubst == nil {
		_ = pipe.CloseConsumer()
		_ = pipe.CloseProducer(nil)
		waitErr := entry.wait(ctx, f.wait)
		e.releaseProcSubstFD(fd)
		if waitErr != nil {
			return nil, waitErr
		}
		return subst, entry.settle(ctx, f)
	}
	return subst, nil
}

func (s *procSubstScope) finish(ctx context.Context, f *frame, stmtErr error) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	entries := append([]*procSubstEntry(nil), s.entries...)
	s.entries = nil
	s.mu.Unlock()
	if len(entries) == 0 {
		return nil
	}
	_, stopped := f.state.latch.Stop()
	var incomplete error
	if stmtErr != nil || stopped {
		incomplete = errStageIncomplete
	}
	for _, entry := range entries {
		if entry.part.Operator == ">(" {
			_ = entry.pipe.CloseProducer(incomplete)
		} else {
			_ = entry.pipe.CloseConsumer()
		}
		if stmtErr != nil || stopped {
			entry.cancel()
		}
	}
	var firstErr error
	for _, entry := range entries {
		waitErr := entry.wait(ctx, f.wait)
		f.e.releaseProcSubstFD(entry.fd)
		if waitErr != nil && firstErr == nil {
			firstErr = waitErr
		}
		if err := entry.settle(ctx, f); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (entry *procSubstEntry) settle(ctx context.Context, f *frame) error {
	g := entry.frame
	err := entry.err
	status := g.state.control.Status()
	if entry.part.Operator == "<(" && err == nil && entry.isExpectedConsumerClosure() {
		status = 0
	}
	result := ordinaryResult(status)
	if stop, ok := g.state.latch.Stop(); ok {
		result = stop.Result
	}
	entry.run.settle(ctx, result, err)
	f.publish.mark(result.PublicationEligible)
	if stop, ok := f.state.latch.Stop(); ok {
		if err != nil {
			return err
		}
		return &AdmissionStoppedError{Stop: stop}
	}
	return err
}

func (entry *procSubstEntry) isExpectedConsumerClosure() bool {
	if entry.pipe.Completion().Termination == StreamConsumerClosed {
		if last := entry.frame.state.last; last == nil || errors.Is(last.Diagnostic, ErrConsumerClosed) {
			return true
		}
	}
	if last := entry.frame.state.last; last != nil && last.Command == "external" {
		var executor ExecutorClosure
		if errors.As(last.Diagnostic, &executor) && executor.ExpectedConsumerClosure() {
			return true
		}
	}
	return false
}

func (e *evaluation) allocateProcSubstFD() (int, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.procSubstFDs == nil {
		e.procSubstFDs = map[int]bool{}
	}
	fd := 63
	for fd >= 10 && e.procSubstFDs[fd] {
		fd--
	}
	if fd < 10 {
		fd = 63 + len(e.procSubstFDs)
	}
	e.procSubstFDs[fd] = true
	return fd, fmt.Sprintf("%s%d", procSubstPrefix, fd)
}

func (e *evaluation) releaseProcSubstFD(fd int) {
	e.mu.Lock()
	delete(e.procSubstFDs, fd)
	e.mu.Unlock()
}

func runArithmeticCommand(ctx context.Context, f *frame, s commandsurface.Statement, c commandsurface.ArithmeticCommand) error {
	var value int64
	prevented, err := f.expanding(s.SyntaxLocation, func(g *frame) error {
		var evalErr error
		value, evalErr = g.e.expander.EvaluateArithmeticCommand(ctx, g, c)
		return evalErr
	})
	if prevented || err != nil {
		return err
	}
	status := 1
	if value != 0 {
		status = 0
	}
	return f.builtinStatus(s.SyntaxLocation, "((", status)
}

func runTrapCommand(f *frame, s commandsurface.Statement, c commandsurface.TrapCommand) error {
	f.state.exitTrap = c.Statements
	return f.builtinStatus(s.SyntaxLocation, "trap", 0)
}

func runExitTrap(ctx context.Context, f *frame, bodyErr error) error {
	stmts := f.state.exitTrap
	f.state.exitTrap = nil
	if len(stmts) == 0 {
		return nil
	}
	if _, stopped := f.state.latch.Stop(); stopped || bodyErr != nil {
		//nolint:nilerr // Trap bodies do not run when the program body already failed with a host or syntax error.
		return nil
	}
	savedStatus := f.state.control.Status()
	savedExited := f.state.control.Exited()
	f.state.control.ResumeForTrap()
	_, err := f.statements(ctx, stmts)
	if err != nil {
		return err
	}
	if !f.state.control.Exited() {
		f.state.control.RestoreAfterTrap(savedStatus, savedExited)
	}
	return nil
}

func expandAssignmentList(ctx context.Context, f *frame, items []commandsurface.Assignment) ([]assignment, error) {
	out := make([]assignment, 0, len(items))
	for _, item := range items {
		a := assignment{loc: item.SyntaxLocation, name: item.Name, appendMode: item.Append}
		if item.Array != nil {
			fields, err := f.expandWords(ctx, item.Array)
			if err != nil {
				return nil, err
			}
			a.array, a.items = true, fields.Values()
			out = append(out, a)
			continue
		}
		if item.Index != nil {
			index, err := f.e.expander.EvaluateArithmeticNode(ctx, f, item.Index, item.Span)
			if err != nil {
				return nil, err
			}
			a.index, a.hasIndex = index, true
		}
		value, err := f.e.expander.ExpandScalar(ctx, f, item.Value)
		if err != nil {
			return nil, err
		}
		a.value = value
		out = append(out, a)
	}
	return out, nil
}

func (a assignment) apply(ctx context.Context, vars *Variables) error {
	switch {
	case a.array:
		return vars.AssignArray(ctx, a.name, a.items, a.appendMode)
	case a.hasIndex:
		return vars.AssignIndex(ctx, a.name, a.index, a.value, a.appendMode)
	default:
		return vars.AssignScalar(ctx, a.name, a.value, a.appendMode)
	}
}

func (entry *procSubstEntry) complete() {
	entry.completionMu.Lock()
	defer entry.completionMu.Unlock()
	if entry.unblock != nil {
		entry.unblock()
		entry.unblock = nil
	}
	close(entry.done)
}

func (entry *procSubstEntry) wait(ctx context.Context, activity *ExecutionWait) error {
	entry.completionMu.Lock()
	select {
	case <-entry.done:
		entry.completionMu.Unlock()
		return nil
	default:
	}
	unblock, err := activity.Block(ctx, WaitState{})
	if err != nil {
		entry.completionMu.Unlock()
		entry.cancel()
		<-entry.done
		return err
	}
	entry.unblock = unblock
	entry.completionMu.Unlock()
	<-entry.done
	return nil
}
