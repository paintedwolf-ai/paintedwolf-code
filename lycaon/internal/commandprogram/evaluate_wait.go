package commandprogram

import (
	"context"
	"sort"
	"sync"
	"time"
)

// executionWaits tracks concurrent evaluator activities while the foreground lives.
// A nil state is runnable; an empty state waits on another activity's stream.
type executionWaits struct {
	mu         sync.Mutex
	ctx        context.Context
	clock      Clock
	port       ProgramWaits
	live       map[*ExecutionWait]*blockedActivity
	deadlines  map[*ExecutionWait]WaitState
	foreground *ExecutionWait
	err        error
	cancel     context.CancelCauseFunc
	unblock    func()
}

type blockedActivity struct {
	state  WaitState
	prior  *blockedActivity
	active bool
}

// ExecutionWait is one evaluator activity's blocking state. Nested synchronous
// commands share it; concurrent frames register distinct activities.
type ExecutionWait struct {
	set *executionWaits
	job string
}

func newExecutionWaits(ctx context.Context, clock Clock, port ProgramWaits, cancel context.CancelCauseFunc) *executionWaits {
	return &executionWaits{ctx: ctx, clock: clock, port: port, live: map[*ExecutionWait]*blockedActivity{}, deadlines: map[*ExecutionWait]WaitState{}, cancel: cancel}
}

func (w *executionWaits) start() *ExecutionWait {
	a := &ExecutionWait{set: w}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.live[a] = nil
	w.clear()
	return a
}

func (w *executionWaits) clear() {
	if w.unblock != nil {
		w.unblock()
		w.unblock = nil
	}
}

func (a *ExecutionWait) finish() {
	if a == nil {
		return
	}
	w := a.set
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, live := w.live[a]; !live {
		return
	}
	delete(w.live, a)
	delete(w.deadlines, a)
	w.clear()
	if err := w.publish(w.ctx); err != nil {
		w.err = err
		w.cancel(err)
	}
}

// Now reads the same injected clock used by program supervision.
func (a *ExecutionWait) Now() time.Time { return a.clock().Now() }
func (a *ExecutionWait) clock() Clock {
	if a == nil {
		return SystemClock()
	}
	return a.set.clock
}

// Deadline scopes the earliest timer that can end a nested blocking operation.
func (a *ExecutionWait) Deadline(until time.Time, statement string) func() {
	if a == nil {
		return func() {}
	}
	w := a.set
	w.mu.Lock()
	prior, had := w.deadlines[a]
	if prior.Until.IsZero() || until.Before(prior.Until) {
		w.deadlines[a] = WaitState{Until: until, Statement: statement}
	}
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if had {
			w.deadlines[a] = prior
		} else {
			delete(w.deadlines, a)
		}
	}
}

// Block records one actual blocking interval and restores running state once.
// The token check prevents an old unblock from clearing a later wait.
func (a *ExecutionWait) Block(ctx context.Context, state WaitState) (func(), error) {
	if a == nil {
		return func() {}, nil
	}
	w := a.set
	w.mu.Lock()
	defer w.mu.Unlock()
	if deadline, ok := w.deadlines[a]; ok && (state.Until.IsZero() || deadline.Until.Before(state.Until)) {
		state.Until, state.Statement = deadline.Until, deadline.Statement
	}
	key := a
	if _, ok := w.live[key]; !ok {
		return func() {}, nil
	}
	prior := w.live[key]
	state.Jobs = append([]string(nil), state.Jobs...)
	blocked := &blockedActivity{state: state, prior: prior, active: true}
	w.live[key] = blocked
	w.clear()
	if err := w.publish(ctx); err != nil {
		if a == w.foreground {
			w.live[key] = prior
			return nil, err
		}
		if w.err == nil {
			w.err = err
			w.cancel(err)
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			blocked.active = false
			if w.live[key] == blocked {
				prior := blocked.prior
				for prior != nil && !prior.active {
					prior = prior.prior
				}
				w.live[key] = prior
			}
			w.clear()
			if err := w.publish(ctx); err != nil {
				w.err = err
				w.cancel(err)
			}
		})
	}, nil
}

func (w *executionWaits) publish(ctx context.Context) error {
	if w.port == nil {
		return nil
	}
	if _, live := w.live[w.foreground]; !live {
		return nil
	}
	state := WaitState{}
	jobs := map[string]bool{}
	for _, head := range w.live {
		for blocked := head; blocked != nil; blocked = blocked.prior {
			if blocked.active {
				for _, job := range blocked.state.Jobs {
					jobs[job] = true
				}
			}
		}
	}
	foregroundReason := ""
	for activity, head := range w.live {
		if head == nil {
			return nil
		}
		for blocked := head; blocked != nil; blocked = blocked.prior {
			if !blocked.active {
				continue
			}
			fact := blocked.state
			if fact.process && fact.Until.IsZero() && !jobs[activity.job] {
				return nil
			}
			if !fact.Until.IsZero() && (state.Until.IsZero() || fact.Until.Before(state.Until) || fact.Until.Equal(state.Until) && fact.Statement < state.Statement) {
				state.Until, state.Statement = fact.Until, fact.Statement
			}
			if activity == w.foreground && len(fact.Jobs) > 0 {
				foregroundReason = fact.Statement
			}
			if state.Statement == "" && len(fact.Jobs) > 0 {
				state.Statement = fact.Statement
			}
			for _, job := range fact.Jobs {
				jobs[job] = true
			}
		}
	}
	if foregroundReason != "" {
		state.Statement = foregroundReason
	}
	if state.Until.IsZero() && len(jobs) == 0 {
		return nil
	}
	for job := range jobs {
		state.Jobs = append(state.Jobs, job)
	}
	sort.Strings(state.Jobs)
	unblock, err := w.port.Block(ctx, state)
	if err == nil {
		w.unblock = unblock
	}
	return err
}

// Until waits for the full timer interval; unrelated events never finish it.
func (a *ExecutionWait) Until(ctx context.Context, until time.Time, statement string) error {
	duration := until.Sub(a.clock().Now())
	if duration <= 0 {
		return ctx.Err()
	}
	done := make(chan struct{})
	var mu sync.Mutex
	ready := false
	var unblock func()
	stop := a.clock().AfterFunc(duration, func() {
		mu.Lock()
		ready = true
		if unblock != nil {
			unblock()
			unblock = nil
		}
		mu.Unlock()
		close(done)
	})
	defer stop()
	mu.Lock()
	if ready {
		mu.Unlock()
		return nil
	}
	var err error
	unblock, err = a.Block(ctx, WaitState{Until: until, Statement: statement})
	mu.Unlock()
	if err != nil {
		return err
	}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		if unblock != nil {
			unblock()
		}
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Fork registers a concurrent activity before its goroutine can run.
func (a *ExecutionWait) Fork() (*ExecutionWait, func()) {
	if a == nil {
		return nil, func() {}
	}
	child := a.set.start()
	a.set.mu.Lock()
	child.job = a.job
	if deadline, ok := a.set.deadlines[a]; ok {
		a.set.deadlines[child] = deadline
	}
	a.set.mu.Unlock()
	return child, child.finish
}

// Job associates a supervised callback's exact handle before its body runs.
func (a *ExecutionWait) Job(handle string) {
	if a == nil {
		return
	}
	a.set.mu.Lock()
	a.job = handle
	a.set.mu.Unlock()
}

// Process observes supervised completion without guessing whether the child runs.
// Only a deadline or an explicit wait on its job makes this a program wait.
func (a *ExecutionWait) Process(ctx context.Context) (func(), error) {
	return a.Block(ctx, WaitState{process: true})
}
