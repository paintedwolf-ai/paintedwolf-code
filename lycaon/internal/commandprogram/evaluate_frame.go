package commandprogram

import (
	"context"
	"io"
	"sync"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// frameState is what statements sharing one frame observe and mutate: braces,
// function bodies and sourced units share it; ( ), $( ), pipeline stages and
// background jobs receive an isolated copy with their own status control.
type declaredFunction struct {
	decl       commandsurface.FunctionDeclaration
	unit       string
	sourceUnit string
}

type frameState struct {
	vars      *Variables
	functions map[string]declaredFunction
	latch     *OutcomeLatch
	control   *StatusControl
	dispatch  Dispatch
	lastJob   string
	hasJob    bool
	// last is the most recent settled execution in this state; a pipeline
	// stage reports it as the stage producer.
	last       *ChildRecord
	pipeStatus []int
	options    shellOptions
}

// shellOptions are the fixed set options: errexit and nounset. Pipelines
// always report the rightmost failure, so pipefail is not a toggle.
type shellOptions struct{ errexit, nounset bool }

// frame is one lexical evaluation context. Values are copied for nested
// contexts; state, publication and the shared evaluation are pointers.
type frame struct {
	e          *evaluation
	state      *frameState
	parent     toolcontract.OperationInvocation
	unit       string
	streams    Streams
	publish    *eligibility
	positional []string
	// prefix holds a function call's NAME=value prefixes: visible to the body
	// and exported to its dispatches, never stored in the caller's frame.
	prefix     map[string]string
	substs     *substitutionStatus
	loops      int
	functions  int
	sources    int
	returnable bool
	// errexitIgnored marks contexts whose failure never exits: conditions,
	// the non-final side of && and ||, and negated statements.
	errexitIgnored bool
}

// substitutionStatus retains the ordinary statuses of the substitutions one
// command's expansion reached. The first nonzero one prevents its dispatch.
type substitutionStatus struct {
	mu                  sync.Mutex
	last, failed        int
	lastID, failedID    string
	reached, hasFailure bool
}

func (s *substitutionStatus) record(executionID string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reached, s.last, s.lastID = true, status, executionID
	if status != 0 && !s.hasFailure {
		s.hasFailure, s.failed, s.failedID = true, status, executionID
	}
}

// eligibility collects whether every producer under a redirected statement was
// typed publication eligible. Pipeline stages may report concurrently.
type eligibility struct {
	mu sync.Mutex
	ok bool
}

func newEligibility() *eligibility { return &eligibility{ok: true} }
func (p *eligibility) mark(eligible bool) {
	p.mu.Lock()
	p.ok = p.ok && eligible
	p.mu.Unlock()
}
func (p *eligibility) eligible() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ok
}

func (e *evaluation) rootFrame(ctx context.Context) (*frame, func(), error) {
	latch := &OutcomeLatch{}
	control, err := NewStatusControl(latch)
	if err != nil {
		return nil, nil, err
	}
	vars, err := NewVariables(ctx, e.ports.Memory, e.programName, e.ports.Environment, nil)
	if err != nil {
		return nil, nil, err
	}
	state := &frameState{vars: vars, functions: map[string]declaredFunction{}, latch: latch, control: control, dispatch: e.ports.Dispatch}
	f := &frame{e: e, state: state, parent: e.ports.Invocation, unit: e.programName, streams: e.ports.Streams.withDefaults(), publish: newEligibility()}
	return f, vars.Close, nil
}

// isolate copies variables and functions into a new state with its own status
// control and a forked directory. A nil latch shares the parent's host stops.
func (f *frame) isolate(ctx context.Context, latch *OutcomeLatch) (*frame, func(), error) {
	if latch == nil {
		latch = f.state.latch
	}
	control, err := NewStatusControl(latch)
	if err != nil {
		return nil, nil, err
	}
	control.status = f.state.control.Status()
	vars, err := f.state.vars.Clone(ctx)
	if err != nil {
		return nil, nil, err
	}
	for name, value := range f.prefix {
		if err = vars.Assign(ctx, name, value); err == nil {
			err = vars.Export(ctx, name)
		}
		if err != nil {
			vars.Close()
			return nil, nil, err
		}
	}
	dispatch, releaseDispatch, err := f.state.dispatch.Fork(ctx)
	if err != nil {
		vars.Close()
		return nil, nil, err
	}
	functions := make(map[string]declaredFunction, len(f.state.functions))
	for name, decl := range f.state.functions {
		functions[name] = decl
	}
	g := *f
	g.state = &frameState{vars: vars, functions: functions, latch: latch, control: control, dispatch: dispatch, lastJob: f.state.lastJob, hasJob: f.state.hasJob, pipeStatus: append([]int(nil), f.state.pipeStatus...), options: f.state.options}
	g.publish, g.prefix, g.substs = newEligibility(), nil, nil
	return &g, func() { releaseDispatch(); vars.Close() }, nil
}

// ExpansionScope implementation: expansion reads, never assigns.

func (f *frame) Variable(name string) (string, bool) {
	if value, ok := f.prefix[name]; ok {
		return value, true
	}
	return f.state.vars.Variable(name)
}
func (f *frame) ProgramName() string  { return f.unit }
func (f *frame) Nounset() bool        { return f.state.options.nounset }
func (f *frame) Positional() []string { return append([]string(nil), f.positional...) }
func (f *frame) LastStatus() int      { return f.state.control.Status() }
func (f *frame) PipeStatus() []int {
	if len(f.state.pipeStatus) > 0 {
		return append([]int(nil), f.state.pipeStatus...)
	}
	return []int{f.state.control.Status()}
}
func (f *frame) LastBackground() (string, bool) {
	return f.state.lastJob, f.state.hasJob
}

// environment is the exported NAME=value list for one dispatch, with this
// frame's function prefixes and the statement's own prefixes applied to that
// dispatch only.
func (f *frame) environment(prefix []assignment) ([]string, error) {
	overrides := make(map[string]string, len(f.prefix)+len(prefix))
	for name, value := range f.prefix {
		overrides[name] = value
	}
	for _, a := range prefix {
		overrides[a.name] = a.value
	}
	return f.state.vars.Environment(overrides)
}

type assignment struct {
	loc         commandsurface.SyntaxLocation
	name, value string
}

// syncWriter serializes concurrent stage writes to one shared stream.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(b)
}
