package heldcall

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// DefaultBudget is how long a call stays in the foreground before it is held.
// It matches the wait command jobs are given.
const DefaultBudget = 30 * time.Second

const (
	// MaxRunningPerSession bounds calls held at once for one session.
	MaxRunningPerSession = 4
	// MaxSettledPerSession bounds settled results kept for held_result.
	MaxSettledPerSession = 16
)

// ErrNotFound reports a handle this registry does not hold.
var ErrNotFound = errors.New("held call handle not found")

// DuplicateError names a held call that already runs the same tool and arguments.
type DuplicateError struct{ Handle string }

func (e *DuplicateError) Error() string { return "identical call already held as " + e.Handle }

// CapacityError names the calls that fill the session's held-call capacity.
type CapacityError struct {
	Limit   int
	Handles []string
}

func (e *CapacityError) Error() string {
	return fmt.Sprintf("held calls at capacity (%d): %s", e.Limit, strings.Join(e.Handles, ", "))
}

// Result is what the caller receives for a call that outlived its budget. Its
// shape follows a promoted command's running result.
type Result struct {
	Running  bool   `json:"running"`
	Handle   string `json:"handle"`
	Tool     string `json:"tool"`
	WaitedMs int64  `json:"waited_ms"`
}

// Publisher emits a handle's lifecycle on the background process topic.
type Publisher func(ctx context.Context, projectID, sessionID string, event api.BackgroundProcessEvent)

// Spec identifies one call to supervise.
type Spec struct {
	DisplayTitle string
	ProjectID    string
	SessionID    string
	ToolCallID   string
	Tool         string
	ArgsDigest   string
	// Budget is how long the caller waits before the call is held.
	Budget time.Duration
}

// Settled is a call's terminal result as its original tool produced it.
type Settled struct {
	Tool    string
	Content string
	Outcome api.ToolResultOutcome
	Facts   guidance.ToolResultFacts
}

// Func runs the call under the supervised context.
type Func func(ctx context.Context) Settled

// Outcome is either a result reached inside the budget or a held handle.
type Outcome struct {
	Settled *Settled
	Handle  string
	Waited  time.Duration
}

// Status is a held call's state.
type Status struct {
	DisplayTitle string
	Handle       string
	Tool         string
	Known        bool
	Running      bool
	Stopped      bool
	Settled      *Settled
	Elapsed      time.Duration
	ExitCode     int
}

// StopResult reports a stop request.
type StopResult struct {
	Handle  string
	Running bool
}

type call struct {
	displayTitle string
	handle       string
	tool         string
	toolCallID   string
	projectID    string
	digest       string
	started      time.Time
	held         bool
	stopped      bool
	cancel       context.CancelFunc
	done         chan struct{}
	settled      *Settled
	settledAt    time.Time
}

type sessionState struct {
	next  int
	calls map[string]*call
}

// Registry supervises held calls for every session.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*sessionState
	publish  Publisher
	onSettle func(sessionID, handle string)
	closed   bool
	wg       sync.WaitGroup
	now      func() time.Time
}

// New builds a registry. publish and onSettle may be nil.
func New(publish Publisher, onSettle func(sessionID, handle string)) *Registry {
	return &Registry{sessions: make(map[string]*sessionState), publish: publish, onSettle: onSettle, now: time.Now}
}

// Run supervises fn, returning its result when it settles within the budget
// and a handle when it does not. A canceled ctx cancels the call and returns
// what it settled with.
func (r *Registry) Run(ctx context.Context, spec Spec, fn Func) (Outcome, error) {
	c, err := r.start(ctx, spec, fn)
	if err != nil {
		return Outcome{}, err
	}
	timer := time.NewTimer(spec.Budget)
	defer timer.Stop()
	select {
	case <-c.done:
		r.discard(spec.SessionID, c)
		return Outcome{Settled: c.settled}, nil
	case <-timer.C:
		if r.promote(ctx, spec.SessionID, c) {
			return Outcome{Handle: c.handle, Waited: spec.Budget}, nil
		}
		<-c.done
		r.discard(spec.SessionID, c)
		return Outcome{Settled: c.settled}, nil
	case <-ctx.Done():
		c.cancel()
		<-c.done
		r.discard(spec.SessionID, c)
		return Outcome{Settled: c.settled}, nil
	}
}

func (r *Registry) start(ctx context.Context, spec Spec, fn Func) (*call, error) {
	sessionID := strings.TrimSpace(spec.SessionID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.New("held call registry closed")
	}
	state := r.sessions[sessionID]
	if state == nil {
		state = &sessionState{calls: make(map[string]*call)}
		r.sessions[sessionID] = state
	}
	var running []string
	for _, existing := range state.calls {
		if existing.held && existing.settled == nil {
			if existing.tool == spec.Tool && existing.digest == spec.ArgsDigest {
				return nil, &DuplicateError{Handle: existing.handle}
			}
			running = append(running, existing.handle)
		}
	}
	if len(running) >= MaxRunningPerSession {
		sort.Strings(running)
		return nil, &CapacityError{Limit: MaxRunningPerSession, Handles: running}
	}
	state.next++
	opCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c := &call{
		displayTitle: spec.DisplayTitle,
		handle:       fmt.Sprintf("held-%d", state.next), tool: spec.Tool, toolCallID: spec.ToolCallID,
		projectID: spec.ProjectID, digest: spec.ArgsDigest, started: r.now(),
		cancel: cancel, done: make(chan struct{}),
	}
	state.calls[c.handle] = c
	r.wg.Add(1)
	go r.execute(opCtx, sessionID, c, fn)
	return c, nil
}

func (r *Registry) execute(ctx context.Context, sessionID string, c *call, fn Func) {
	defer r.wg.Done()
	result := func() (settled Settled) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "held call panicked", "tool", c.tool, "handle", c.handle, "panic", recovered)
				settled = Settled{
					Tool: c.tool, Content: fmt.Sprintf("tool %s panicked", c.tool), Outcome: api.ToolResultOutcomeError,
				}
			}
		}()
		return fn(ctx)
	}()
	if result.Tool == "" {
		result.Tool = c.tool
	}
	r.mu.Lock()
	c.settled = &result
	c.settledAt = r.now()
	held := c.held
	r.pruneLocked(sessionID)
	r.mu.Unlock()
	close(c.done)
	c.cancel()
	if !held {
		return
	}
	r.publishExit(ctx, sessionID, c, result.Outcome)
	if r.onSettle != nil {
		r.onSettle(sessionID, c.handle)
	}
}

func (r *Registry) publishExit(ctx context.Context, sessionID string, c *call, outcome api.ToolResultOutcome) {
	if r.publish == nil {
		return
	}
	code := 0
	if outcome != api.ToolResultOutcomeCompleted {
		code = 1
	}
	r.publish(context.WithoutCancel(ctx), c.projectID, sessionID, api.BackgroundProcessEvent{
		ProcessID: c.handle, SessionID: sessionID, Stream: "exit", Running: false, ExitCode: &code,
	})
}

// promote makes a running call visible as a handle, reporting false when it
// settled first.
func (r *Registry) promote(ctx context.Context, sessionID string, c *call) bool {
	r.mu.Lock()
	if c.settled != nil {
		r.mu.Unlock()
		return false
	}
	c.held = true
	r.mu.Unlock()
	if r.publish != nil {
		r.publish(context.WithoutCancel(ctx), c.projectID, sessionID, api.BackgroundProcessEvent{
			ProcessID: c.handle, SessionID: sessionID, Stream: "stdout", Running: true,
		})
	}
	return true
}

// discard drops a call that settled before it was held.
func (r *Registry) discard(sessionID string, c *call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.held {
		return
	}
	if state := r.sessions[strings.TrimSpace(sessionID)]; state != nil {
		delete(state.calls, c.handle)
	}
}

// pruneLocked evicts the oldest settled results past the retention bound.
func (r *Registry) pruneLocked(sessionID string) {
	state := r.sessions[sessionID]
	if state == nil {
		return
	}
	var settled []*call
	for _, c := range state.calls {
		if c.held && c.settled != nil {
			settled = append(settled, c)
		}
	}
	if len(settled) <= MaxSettledPerSession {
		return
	}
	sort.Slice(settled, func(i, j int) bool { return settled[i].settledAt.Before(settled[j].settledAt) })
	for _, c := range settled[:len(settled)-MaxSettledPerSession] {
		delete(state.calls, c.handle)
	}
}

func (r *Registry) lookupLocked(sessionID, handle string) *call {
	state := r.sessions[strings.TrimSpace(sessionID)]
	if state == nil {
		return nil
	}
	c := state.calls[strings.TrimSpace(handle)]
	if c == nil || !c.held {
		return nil
	}
	return c
}

// State reports whether the handle is held here and whether it still runs.
func (r *Registry) State(sessionID, handle string) (known, running bool) {
	if r == nil {
		return false, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.lookupLocked(sessionID, handle)
	if c == nil {
		return false, false
	}
	return true, c.settled == nil
}

// Status returns one held call's state, including its result once settled.
func (r *Registry) Status(sessionID, handle string) (Status, error) {
	if r == nil {
		return Status{}, ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.lookupLocked(sessionID, handle)
	if c == nil {
		return Status{}, ErrNotFound
	}
	status := Status{DisplayTitle: c.displayTitle, Handle: c.handle, Tool: c.tool, Known: true, Running: c.settled == nil, Stopped: c.stopped}
	if c.settled != nil {
		copied := *c.settled
		status.Settled = &copied
		status.Elapsed = c.settledAt.Sub(c.started)
	} else {
		status.Elapsed = r.now().Sub(c.started)
	}
	return status, nil
}

// Await waits up to d for the handle to settle and returns its state. A
// canceled ctx or an elapsed d returns the state as it stands.
func (r *Registry) Await(ctx context.Context, sessionID, handle string, d time.Duration) (Status, error) {
	if r == nil {
		return Status{}, ErrNotFound
	}
	r.mu.Lock()
	c := r.lookupLocked(sessionID, handle)
	r.mu.Unlock()
	if c == nil {
		return Status{}, ErrNotFound
	}
	if d > 0 {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-c.done:
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	return r.Status(sessionID, handle)
}

// HasHandles reports whether the session still holds any handle, running or
// settled, so the controls that read or stop it stay reachable.
func (r *Registry) HasHandles(sessionID string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.sessions[strings.TrimSpace(sessionID)]
	if state == nil {
		return false
	}
	for _, c := range state.calls {
		if c.held {
			return true
		}
	}
	return false
}

// Running describes one held call still in flight.
type Running struct {
	Handle  string
	Tool    string
	Elapsed time.Duration
}

// Ledger lists the session's running held calls in handle order.
func (r *Registry) Ledger(sessionID string) []Running {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.sessions[strings.TrimSpace(sessionID)]
	if state == nil {
		return nil
	}
	now := r.now()
	var out []Running
	for _, c := range state.calls {
		if c.held && c.settled == nil {
			out = append(out, Running{Handle: c.handle, Tool: c.tool, Elapsed: now.Sub(c.started)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out
}

// HasRunningHandles reports whether any listed handle still runs here. With no
// handles it reports whether any held call runs.
func (r *Registry) HasRunningHandles(sessionID string, handles []string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.sessions[strings.TrimSpace(sessionID)]
	if state == nil {
		return false
	}
	if len(handles) == 0 {
		for _, c := range state.calls {
			if c.held && c.settled == nil {
				return true
			}
		}
		return false
	}
	for _, handle := range handles {
		if c := state.calls[strings.TrimSpace(handle)]; c != nil && c.held && c.settled == nil {
			return true
		}
	}
	return false
}

// List returns the session's held handles in handle order.
func (r *Registry) List(sessionID string) []api.BackgroundProcess {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.sessions[strings.TrimSpace(sessionID)]
	if state == nil {
		return nil
	}
	var out []api.BackgroundProcess
	for _, c := range state.calls {
		if !c.held {
			continue
		}
		item := api.BackgroundProcess{ProcessID: c.handle, Running: c.settled == nil}
		if c.settled != nil {
			code := 0
			if c.settled.Outcome != api.ToolResultOutcomeCompleted {
				code = 1
			}
			item.ExitCode = &code
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProcessID < out[j].ProcessID })
	return out
}

// Stop cancels a running held call. The call settles with what its tool
// returns once it observes the cancellation.
func (r *Registry) Stop(sessionID, handle string) (StopResult, error) {
	if r == nil {
		return StopResult{}, ErrNotFound
	}
	r.mu.Lock()
	c := r.lookupLocked(sessionID, handle)
	if c == nil {
		r.mu.Unlock()
		return StopResult{}, ErrNotFound
	}
	running := c.settled == nil
	if running {
		c.stopped = true
	}
	r.mu.Unlock()
	if running {
		c.cancel()
	}
	return StopResult{Handle: c.handle, Running: running}, nil
}

// DisposeSession cancels and forgets every call held for the session.
func (r *Registry) DisposeSession(sessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	state := r.sessions[strings.TrimSpace(sessionID)]
	delete(r.sessions, strings.TrimSpace(sessionID))
	r.mu.Unlock()
	if state == nil {
		return
	}
	for _, c := range state.calls {
		c.cancel()
	}
}

// Close rejects new calls, cancels every held call, and waits for them.
func (r *Registry) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.closed = true
	var all []*call
	for _, state := range r.sessions {
		for _, c := range state.calls {
			all = append(all, c)
		}
	}
	r.mu.Unlock()
	for _, c := range all {
		c.cancel()
	}
	finished := make(chan struct{})
	go func() { r.wg.Wait(); close(finished) }()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
