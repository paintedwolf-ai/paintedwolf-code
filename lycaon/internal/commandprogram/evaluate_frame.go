package commandprogram

import (
	"context"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// frameState is what statements sharing one frame observe and mutate: braces,
// function bodies and sourced units share it; ( ), $( ), pipeline stages and
// background jobs receive an isolated copy with their own status control.
type frameState struct {
	vars      *variables
	functions map[string]commandsurface.FunctionDeclaration
	latch     *OutcomeLatch
	control   *StatusControl
	dispatch  Dispatch
	lastJob   string
	hasJob    bool
	// last is the most recent settled execution in this state; a pipeline
	// stage reports it as the stage producer.
	last *ChildRecord
}

// frame is one lexical evaluation context. Values are copied for nested
// contexts; state, publication and the shared evaluation are pointers.
type frame struct {
	e          *evaluation
	state      *frameState
	parent     toolcontract.OperationInvocation
	streams    Streams
	publish    *eligibility
	positional []string
	loops      int
	functions  int
	sources    int
	substs     int
	returnable bool
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
	vars := &variables{memory: e.ports.Memory, limits: e.limits, values: map[string]*variable{}}
	state := &frameState{vars: vars, functions: map[string]commandsurface.FunctionDeclaration{}, latch: latch, control: control, dispatch: e.ports.Dispatch}
	f := &frame{e: e, state: state, parent: e.ports.Invocation, streams: e.ports.Streams.withDefaults(), publish: newEligibility()}
	return f, vars.release, nil
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
	vars, err := f.state.vars.copy(ctx)
	if err != nil {
		return nil, nil, err
	}
	dispatch, releaseDispatch, err := f.state.dispatch.Fork(ctx)
	if err != nil {
		vars.release()
		return nil, nil, err
	}
	functions := make(map[string]commandsurface.FunctionDeclaration, len(f.state.functions))
	for name, decl := range f.state.functions {
		functions[name] = decl
	}
	g := *f
	g.state = &frameState{vars: vars, functions: functions, latch: latch, control: control, dispatch: dispatch, lastJob: f.state.lastJob, hasJob: f.state.hasJob}
	g.publish = newEligibility()
	return &g, func() { releaseDispatch(); vars.release() }, nil
}

// ExpansionScope implementation: expansion reads, never assigns.

func (f *frame) Variable(name string) (string, bool) {
	if name == "0" {
		return f.e.programName, true
	}
	return f.state.vars.get(name)
}
func (f *frame) Positional() []string { return append([]string(nil), f.positional...) }
func (f *frame) LastStatus() int      { return f.state.control.Status() }
func (f *frame) LastBackground() (string, bool) {
	return f.state.lastJob, f.state.hasJob
}

// environment is the exported NAME=value list for one dispatch, with
// statement-prefix assignments applied to that dispatch only.
func (f *frame) environment(prefix []assignment) []string {
	values := f.state.vars.exported()
	for _, a := range prefix {
		values[a.name] = a.value
	}
	out := make([]string, 0, len(values))
	for name, value := range values {
		out = append(out, name+"="+value)
	}
	sort.Strings(out)
	return out
}

type assignment struct {
	loc         commandsurface.SyntaxLocation
	name, value string
}

type variable struct {
	value    string
	set      bool
	exported bool
	release  func()
}

// variables is one frame's invocation-local store. Each entry's bytes are
// reserved against program_state_bytes before insertion, old and new values
// overlapping until the replacement is admitted.
type variables struct {
	memory *toolcontract.ProgramMemory
	limits evaluationLimits
	values map[string]*variable
}

func (v *variables) get(name string) (string, bool) {
	entry, ok := v.values[name]
	if !ok || !entry.set {
		return "", false
	}
	return entry.value, true
}

func (v *variables) exported() map[string]string {
	out := map[string]string{}
	for name, entry := range v.values {
		if entry.set && entry.exported {
			out[name] = entry.value
		}
	}
	return out
}

func (v *variables) set(ctx context.Context, name, value string, export bool) error {
	old, exists := v.values[name]
	if !exists && int64(len(v.values)+1) > v.limits["variable_count"] {
		return &ProgramLimitError{Limit: "variable_count", Maximum: v.limits["variable_count"], Observed: int64(len(v.values) + 1)}
	}
	if int64(len(value)) > v.limits["variable_value_bytes"] {
		return &ProgramLimitError{Limit: "variable_value_bytes", Maximum: v.limits["variable_value_bytes"], Observed: int64(len(value))}
	}
	release, err := v.memory.ReserveProgramState(ctx, int64(len(name)+len(value))+64)
	if err != nil {
		return err
	}
	entry := &variable{value: strings.Clone(value), set: true, exported: export, release: release}
	if exists {
		entry.exported = entry.exported || old.exported
		old.release()
	}
	v.values[name] = entry
	return nil
}

func (v *variables) export(ctx context.Context, name string) error {
	if entry, ok := v.values[name]; ok {
		entry.exported = true
		return nil
	}
	if int64(len(v.values)+1) > v.limits["variable_count"] {
		return &ProgramLimitError{Limit: "variable_count", Maximum: v.limits["variable_count"], Observed: int64(len(v.values) + 1)}
	}
	release, err := v.memory.ReserveProgramState(ctx, int64(len(name))+64)
	if err != nil {
		return err
	}
	v.values[name] = &variable{exported: true, release: release}
	return nil
}

func (v *variables) unset(name string) {
	if entry, ok := v.values[name]; ok {
		entry.release()
		delete(v.values, name)
	}
}

func (v *variables) copy(ctx context.Context) (*variables, error) {
	out := &variables{memory: v.memory, limits: v.limits, values: make(map[string]*variable, len(v.values))}
	for name, entry := range v.values {
		release, err := v.memory.ReserveProgramState(ctx, int64(len(name)+len(entry.value))+64)
		if err != nil {
			out.release()
			return nil, err
		}
		copied := *entry
		copied.release = release
		out.values[name] = &copied
	}
	return out, nil
}

func (v *variables) release() {
	for name, entry := range v.values {
		entry.release()
		delete(v.values, name)
	}
}

// boundedCapture is a substitution's stdout. Writes past substitution_bytes are
// refused before they are retained.
type boundedCapture struct {
	mu       sync.Mutex
	data     []byte
	maximum  int64
	overflow int64
}

func (c *boundedCapture) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if int64(len(c.data)+len(b)) > c.maximum {
		c.overflow = int64(len(c.data) + len(b))
		return 0, &StreamLimitError{Limit: "substitution_bytes", Maximum: c.maximum, Observed: c.overflow}
	}
	c.data = append(c.data, b...)
	return len(b), nil
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
