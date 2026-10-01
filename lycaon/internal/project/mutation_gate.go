package project

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/noticeerr"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var ErrMutationInProgress error = noticeerr.NewSentinel("project lifecycle mutation in progress", wire.NoticeCodeProjectMutationInProgress)

// MutationGate serializes project lifecycle transitions with runtime work.
type MutationGate struct {
	mu        sync.Mutex
	mutations map[string]*mutationState
	runtimes  map[string]int
}

type mutationState struct {
	drained chan struct{}
	closed  bool
}

func NewMutationGate() *MutationGate {
	return &MutationGate{
		mutations: make(map[string]*mutationState),
		runtimes:  make(map[string]int),
	}
}

// BeginMutation acquires the project's exclusive lifecycle lock.
func (g *MutationGate) BeginMutation(projectID string) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.mutations[projectID]; ok {
		return ErrMutationInProgress
	}
	if g.runtimes[projectID] > 0 {
		return ErrProjectBusy
	}
	g.mutations[projectID] = newMutationState(true)
	return nil
}

// BeginDrainingMutation blocks admission and returns the runtime drain wait.
func (g *MutationGate) BeginDrainingMutation(projectID string) (func(context.Context) error, error) {
	if g == nil {
		return func(context.Context) error { return nil }, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.mutations[projectID]; ok {
		return nil, ErrMutationInProgress
	}
	state := newMutationState(g.runtimes[projectID] == 0)
	g.mutations[projectID] = state
	return func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-state.drained:
			return nil
		}
	}, nil
}

func newMutationState(drained bool) *mutationState {
	state := &mutationState{drained: make(chan struct{})}
	if drained {
		close(state.drained)
		state.closed = true
	}
	return state
}

func (g *MutationGate) EndMutation(projectID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.mutations, projectID)
	g.mu.Unlock()
}

// MutationDrained reports whether the lifecycle lock has no runtimes.
func (g *MutationGate) MutationDrained(projectID string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.mutations[projectID]
	return state != nil && state.closed
}

// BeginRuntime registers a runtime until the returned release runs.
func (g *MutationGate) BeginRuntime(projectID string) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	g.mu.Lock()
	if _, ok := g.mutations[projectID]; ok {
		g.mu.Unlock()
		return nil, ErrMutationInProgress
	}
	g.runtimes[projectID]++
	g.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			if g.runtimes[projectID] <= 1 {
				delete(g.runtimes, projectID)
				if state := g.mutations[projectID]; state != nil && !state.closed {
					close(state.drained)
					state.closed = true
				}
			} else {
				g.runtimes[projectID]--
			}
			g.mu.Unlock()
		})
	}, nil
}
