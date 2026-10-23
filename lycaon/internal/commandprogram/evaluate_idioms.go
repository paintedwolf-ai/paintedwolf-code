package commandprogram

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/commandsurface"
)

type procSubstConsumer struct {
	part commandsurface.SubstitutionPart
	path string
}

type procSubstScope struct {
	mu        sync.Mutex
	consumers []procSubstConsumer
}

func (s *procSubstScope) addConsumer(part commandsurface.SubstitutionPart, path string) {
	s.mu.Lock()
	s.consumers = append(s.consumers, procSubstConsumer{part: part, path: path})
	s.mu.Unlock()
}

// ProcessSubstitute evaluates a reached <(list) or >(list) unit in an isolated
// frame with its own child receipt and resource bounds, returning the scratch
// path passed to the consuming command.
func (e *evaluation) ProcessSubstitute(ctx context.Context, scope ExpansionScope, request SubstitutionRequest) (string, error) {
	f, ok := scope.(*frame)
	if !ok || f.e != e {
		return "", errors.New("process substitution scope is not a frame of this evaluation")
	}
	part := request.Unit
	path, file, err := e.newScratchFile()
	if err != nil {
		return "", err
	}
	if part.Operator == ">(" {
		_ = file.Close()
		if f.procSubst != nil {
			f.procSubst.addConsumer(part, path)
		}
		return path, nil
	}
	run, err := f.beginRecorded(ctx, part.SyntaxLocation, "<(")
	if err != nil {
		_ = file.Close()
		return "", err
	}
	defer run.close()
	g, release, err := f.isolate(ctx, nil)
	if err != nil {
		_ = file.Close()
		return "", f.expansionRefusal(part.SyntaxLocation, err)
	}
	defer release()
	g.parent, g.loops, g.returnable = run.inv, 0, false
	g.state.options.errexit = false
	writer := &boundedProcessSubstWriter{frame: g, loc: part.SyntaxLocation, file: file, maximum: e.limits["stream_bytes"]}
	g.streams = f.streams
	g.streams.SetReader(0, NullInput())
	g.streams.SetWriter(1, writer)
	g.streams.SetWriter(2, f.streams.Error)
	_, err = g.statements(ctx, part.Statements)
	if trapErr := runExitTrap(ctx, g, err); trapErr != nil && err == nil {
		err = trapErr
	}
	closeErr := file.Close()
	if err == nil && writer.err != nil {
		err = writer.err
	}
	if err == nil && closeErr != nil {
		err = closeErr
	}
	result := ordinaryResult(g.state.control.Status())
	if stop, ok := g.state.latch.Stop(); ok {
		result = stop.Result
	}
	run.settle(result, err)
	if err != nil {
		return "", &SubstitutionError{Node: part.ID, Span: part.Span, Result: result, Cause: err}
	}
	return path, nil
}

type boundedProcessSubstWriter struct {
	frame   *frame
	loc     commandsurface.SyntaxLocation
	file    *os.File
	maximum int64
	written int64
	err     error
}

func (w *boundedProcessSubstWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	observed := w.written + int64(len(p))
	if observed > w.maximum {
		w.err = w.frame.refuseLimit(w.loc, "stream_bytes", observed)
		return 0, w.err
	}
	n, err := w.file.Write(p)
	w.written += int64(n)
	if err != nil {
		w.err = err
	}
	return n, err
}

func (s *procSubstScope) finish(ctx context.Context, f *frame) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	consumers := append([]procSubstConsumer(nil), s.consumers...)
	s.consumers = nil
	s.mu.Unlock()
	for _, consumer := range consumers {
		if _, stopped := f.state.latch.Stop(); stopped {
			return nil
		}
		if err := s.runConsumer(ctx, f, consumer); err != nil {
			return err
		}
	}
	return nil
}

func (s *procSubstScope) runConsumer(ctx context.Context, f *frame, consumer procSubstConsumer) error {
	part := consumer.part
	file, err := os.Open(consumer.path)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(consumer.path)
	}()
	run, err := f.beginRecorded(ctx, part.SyntaxLocation, ">(")
	if err != nil {
		return err
	}
	defer run.close()
	g, release, err := f.isolate(ctx, nil)
	if err != nil {
		return f.stateRefusal(part.SyntaxLocation, err)
	}
	defer release()
	g.parent, g.loops, g.returnable = run.inv, 0, false
	g.state.options.errexit = false
	g.streams = f.streams
	g.streams.SetReader(0, io.NopCloser(file))
	g.streams.SetWriter(1, f.streams.Output)
	g.streams.SetWriter(2, f.streams.Error)
	_, err = g.statements(ctx, part.Statements)
	if trapErr := runExitTrap(ctx, g, err); trapErr != nil && err == nil {
		err = trapErr
	}
	result := ordinaryResult(g.state.control.Status())
	if stop, ok := g.state.latch.Stop(); ok {
		result = stop.Result
	}
	run.settle(result, err)
	return err
}

func (e *evaluation) newScratchFile() (string, *os.File, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	dir := e.ports.ScratchDir
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", nil, err
		}
	} else {
		if e.tempDir == "" {
			created, err := os.MkdirTemp("", "pw-procsubst-*")
			if err != nil {
				return "", nil, err
			}
			e.tempDir = created
		}
		dir = e.tempDir
	}
	if canonical, err := filepath.EvalSymlinks(dir); err == nil && canonical != "" {
		dir = canonical
	}
	file, err := os.CreateTemp(dir, "procsubst-*")
	if err != nil {
		return "", nil, err
	}
	path := file.Name()
	e.scratchFiles = append(e.scratchFiles, path)
	return path, file, nil
}

func (e *evaluation) cleanupScratch() {
	e.mu.Lock()
	files := e.scratchFiles
	e.scratchFiles = nil
	tempDir := e.tempDir
	e.tempDir = ""
	e.mu.Unlock()
	for _, path := range files {
		_ = os.Remove(path)
	}
	if tempDir != "" {
		_ = os.RemoveAll(tempDir)
	}
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
