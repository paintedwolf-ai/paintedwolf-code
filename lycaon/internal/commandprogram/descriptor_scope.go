package commandprogram

import (
	"context"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// descriptorFrame records actual endpoint replacement. An enclosing redirect
// must not replace a pipe or substitution endpoint created inside its scope.
type descriptorFrame struct {
	streams     Streams
	generations [3]uint64
	scope       *descriptorScope
}

func (d *descriptorFrame) setInput(r io.ReadCloser) { d.streams.Input = r; d.generations[0]++ }
func (d *descriptorFrame) setOutput(w io.Writer)    { d.streams.Output = w; d.generations[1]++ }
func (d *descriptorFrame) setError(w io.Writer)     { d.streams.Error = w; d.generations[2]++ }
func (d descriptorFrame) PrepareStreams(ctx context.Context) (Streams, error) {
	streams := d.streams
	if d.scope == nil {
		return streams, nil
	}
	parent := d
	parent.scope = d.scope.base.scope
	var parentErr error
	streams, parentErr = parent.PrepareStreams(ctx)
	if parentErr != nil {
		return Streams{}, parentErr
	}
	needed := false
	for fd, changed := range d.scope.changed {
		needed = needed || changed && d.generations[fd] == d.scope.base.generations[fd]
	}
	if !needed {
		return streams, nil
	}
	bound, err := d.scope.open(ctx)
	if err != nil {
		return Streams{}, err
	}
	for fd, changed := range d.scope.changed {
		if !changed || d.generations[fd] != d.scope.base.generations[fd] {
			continue
		}
		switch fd {
		case 0:
			streams.Input = bound.Input
		case 1:
			streams.Output = bound.Output
		case 2:
			streams.Error = bound.Error
		}
	}
	return streams, nil
}

// pending reports descriptor work that must cross admission before local state
// changes. Already opened endpoints do not make a local assignment an I/O child.
func (d descriptorFrame) pending() bool {
	for scope := d.scope; scope != nil; scope = scope.base.scope {
		if scope.opened.Load() {
			continue
		}
		for fd, changed := range scope.changed {
			if changed && d.generations[fd] == scope.base.generations[fd] {
				return true
			}
		}
	}
	return false
}

// RequiredEffects describes the descriptors this reached endpoint inherits.
func (d descriptorFrame) RequiredEffects() toolcontract.CommandEffects {
	endpoints := d.endpointEffects()
	effects := endpoints[1] | endpoints[2]
	for scope := d.scope; scope != nil; scope = scope.base.scope {
		if scope.opened.Load() {
			continue
		}
		for fd, changed := range scope.changed {
			if changed && d.generations[fd] == scope.base.generations[fd] {
				effects |= scope.openingEffects
				break
			}
		}
	}
	return effects
}

func (d descriptorFrame) endpointEffects() [3]toolcontract.CommandEffects {
	var effects [3]toolcontract.CommandEffects
	var selected [3]bool
	for scope := d.scope; scope != nil; scope = scope.base.scope {
		for fd, changed := range scope.changed {
			if changed && d.generations[fd] == scope.base.generations[fd] && !selected[fd] {
				effects[fd] = scope.writes[fd]
				selected[fd] = true
			}
		}
	}
	return effects
}

type descriptorPlan struct {
	redirect commandsurface.ProgramRedirect
	target   Field
	body     string
}

// descriptorScope owns one statement's pre-expanded descriptor plan and staged
// resources. Its opening is deferred to an admitted operation; its closing is
// still the lexical statement's existing publication boundary.
type descriptorScope struct {
	frame          frame
	base           descriptorFrame
	plans          []descriptorPlan
	changed        [3]bool
	writes         [3]toolcontract.CommandEffects
	openingEffects toolcontract.CommandEffects
	statementID    commandsurface.NodeID
	lifetime       sync.Mutex
	users          int
	eligible       bool
	once           sync.Once
	opened         atomic.Bool
	prevented      bool
	streams        Streams
	staged         []*stagedRedirect
	err            error
	inputs         []io.ReadCloser
	input          InputRedirection
	output         OutputPublication
	releases       []func()
}

func (s *descriptorScope) prepare(ctx context.Context, f *frame, statement commandsurface.Statement) (bool, error) {
	s.users, s.eligible = 1, true
	s.statementID = statement.ID
	s.frame = *f
	s.input, s.output = f.redirections()
	release, err := f.e.ports.Memory.ReserveProgramState(ctx, 2048+int64(len(statement.Redirects))*512)
	if err != nil {
		return true, err
	}
	s.releases = make([]func(), 0, len(statement.Redirects)+1)
	s.releases = append(s.releases, release)
	s.plans = make([]descriptorPlan, 0, len(statement.Redirects))
	s.inputs = make([]io.ReadCloser, 0, len(statement.Redirects))
	s.staged = make([]*stagedRedirect, 0, len(statement.Redirects))
	prepared := false
	defer func() {
		if !prepared {
			for _, release := range s.releases {
				release()
			}
			s.releases = nil
		}
	}()
	s.base = f.io
	s.writes = s.base.endpointEffects()
	s.frame.publish = newEligibility()
	for _, r := range statement.Redirects {
		plan := descriptorPlan{redirect: r}
		switch r.Operator {
		case ">&":
		case "<<", "<<-":
			prevented, err := f.expanding(r.SyntaxLocation, func(scope *frame) error {
				var err error
				plan.body, err = f.e.expander.ExpandDocument(ctx, scope, r)
				return err
			})
			if prevented || err != nil {
				return true, err
			}
		default:
			prevented, err := f.expanding(r.SyntaxLocation, func(scope *frame) error {
				var err error
				plan.target.Value, err = f.e.expander.ExpandScalar(ctx, scope, r.Target)
				plan.target.Span = r.Target.Span
				return err
			})
			if prevented || err != nil {
				return true, err
			}
		}
		// Expansion owns construction; retain its completed value before storing it.
		release, err := f.e.ports.Memory.ReserveProgramState(ctx, int64(len(plan.target.Value)+len(plan.body)))
		if err != nil {
			return true, err
		}
		s.releases = append(s.releases, release)
		s.plans = append(s.plans, plan)
		mutation, _ := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
		switch r.Operator {
		case ">", ">>":
			s.openingEffects |= mutation
			fd := 1
			if r.Descriptor == 2 {
				fd = 2
			}
			s.writes[fd] = mutation
		case "&>", "&>>":
			s.openingEffects |= mutation
			s.writes[1], s.writes[2] = mutation, mutation
		case ">&":
			if r.Descriptor == 2 {
				s.writes[2] = s.writes[1]
			} else {
				s.writes[1] = s.writes[2]
			}
		}
		switch {
		case r.Operator == "<" || r.Operator == "<<" || r.Operator == "<<-":
			s.changed[0] = true
		case r.Operator == "&>" || r.Operator == "&>>":
			s.changed[1], s.changed[2] = true, true
		case r.Descriptor == 2:
			s.changed[2] = true
		default:
			s.changed[1] = true
		}
	}
	f.io.scope = s
	f.publish = s.frame.publish
	prepared = true
	return false, nil
}
func (s *descriptorScope) open(ctx context.Context) (Streams, error) {
	s.once.Do(func() {
		s.opened.Store(true)
		streams, err := s.base.PrepareStreams(ctx)
		if err != nil {
			s.err = err
			return
		}
		g := s.frame
		g.io = descriptorFrame{streams: streams}
		for _, plan := range s.plans {
			r := plan.redirect
			if r.Operator == "<<" || r.Operator == "<<-" {
				g.io.streams.Input = io.NopCloser(strings.NewReader(plan.body))
				continue
			}
			out, failed, err := s.bind(ctx, &g, r, plan.target)
			if out != nil {
				s.staged = append(s.staged, out)
			}
			if err != nil || failed {
				s.err = err
				if s.err == nil {
					s.err = &descriptorOpenFailure{Status: g.state.control.Status()}
				}
				return
			}
		}
		s.streams = g.io.streams
	})
	return s.streams, s.err
}

// retain keeps inherited descriptors alive while a supervised child uses them.
func (d descriptorFrame) retain() func(context.Context, bool) error {
	var scopes []*descriptorScope
	for scope := d.scope; scope != nil; scope = scope.base.scope {
		scope.lifetime.Lock()
		scope.users++
		scope.lifetime.Unlock()
		scopes = append(scopes, scope)
	}
	return func(ctx context.Context, eligible bool) error {
		var first error
		for _, scope := range scopes {
			if err := scope.finish(ctx, eligible); err != nil && first == nil {
				first = err
			}
		}
		return first
	}
}

func (s *descriptorScope) finish(ctx context.Context, publish bool) error {
	s.lifetime.Lock()
	s.eligible = s.eligible && publish
	s.users--
	ready, eligible := s.users == 0, s.eligible
	s.lifetime.Unlock()
	if !ready {
		return nil
	}
	defer func() {
		for _, release := range s.releases {
			release()
		}
		s.releases = nil
	}()
	defer func() {
		for _, input := range s.inputs {
			_ = input.Close()
		}
	}()
	var first error
	for _, out := range s.staged {
		if err := out.finish(ctx, eligible && first == nil); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// descriptorOpenFailure retains an ordinary redirect status without running the
// producer. It is not a permission decision or a host stop.
type descriptorOpenFailure struct {
	Status int
	Cause  error
}

func (e *descriptorOpenFailure) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return strconv.Itoa(e.Status)
}
func (e *descriptorOpenFailure) ProducerResult() toolcontract.ProducerResult {
	return ordinaryResult(e.Status)
}
