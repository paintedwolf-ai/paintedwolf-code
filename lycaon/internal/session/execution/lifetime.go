package execution

import (
	"context"
	"errors"
	"sync"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectliveness"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptstate"
)

// Lifetime owns prompt exclusion, cancellation, and the project runtime lease.
type Lifetime struct {
	scratch         *scratch.Folders
	processes       interface{ HasRunning(string) bool }
	Prompt          promptstate.MutexRegistry
	Gate            *lifecycle.State
	promptCancelMu  sync.Mutex
	promptCancel    map[string]context.CancelCauseFunc
	mutationGate    *project.MutationGate
	projectLiveness *projectliveness.Tracker
}

func NewLifetime(gate *lifecycle.State) *Lifetime                       { return &Lifetime{Gate: gate} }
func (m *Lifetime) SetMutationGate(gate *project.MutationGate)          { m.mutationGate = gate }
func (m *Lifetime) SetProjectLiveness(tracker *projectliveness.Tracker) { m.projectLiveness = tracker }
func (m *Lifetime) BeginProject(projectID string) (func(), error) {
	if m == nil || m.mutationGate == nil {
		return func() {}, nil
	}
	return m.mutationGate.BeginRuntime(projectID)
}
func (m *Lifetime) ClaimTurn(projectID, turnID string) func() {
	if m == nil || m.projectLiveness == nil {
		return func() {}
	}
	return m.projectLiveness.ClaimTurn(projectID, turnID)
}

func (m *Lifetime) Attach(parent context.Context, sessionID string) context.Context {
	if m == nil {
		return parent
	}
	ctx, cancel := context.WithCancelCause(parent)
	// The stop context ignores the turn deadline.
	stop, stopCancel := context.WithCancel(context.WithoutCancel(parent))
	m.RegisterCancel(sessionID, func(cause error) {
		cancel(cause)
		stopCancel()
	})
	// Catch stop beginning before cancellation registration.
	if m.Gate != nil && m.Gate.InProgress(parent, sessionID) {
		m.CancelByUser(sessionID)
	}
	return hitl.WithStopContext(ctx, stop)
}

var ErrUserCancelled = errors.New("turn stopped by user")

func (m *Lifetime) CancelByUser(sessionID string) { m.cancelWithCause(sessionID, ErrUserCancelled) }
func (m *Lifetime) Cancel(sessionID string)       { m.cancelWithCause(sessionID, context.Canceled) }
func (m *Lifetime) cancelWithCause(sessionID string, cause error) {
	if m == nil {
		return
	}
	m.promptCancelMu.Lock()
	cancel, ok := m.promptCancel[sessionID]
	if ok {
		delete(m.promptCancel, sessionID)
	}
	m.promptCancelMu.Unlock()
	if ok && cancel != nil {
		cancel(cause)
	}
}

func (m *Lifetime) RegisterCancel(sessionID string, cancel context.CancelCauseFunc) {
	if m == nil || cancel == nil {
		return
	}
	m.promptCancelMu.Lock()
	defer m.promptCancelMu.Unlock()
	if m.promptCancel == nil {
		m.promptCancel = make(map[string]context.CancelCauseFunc)
	}
	if prev, ok := m.promptCancel[sessionID]; ok {
		prev(context.Canceled)
	}
	m.promptCancel[sessionID] = cancel
}

func (m *Lifetime) Running(sessionID string) bool {
	if m == nil {
		return false
	}
	m.promptCancelMu.Lock()
	defer m.promptCancelMu.Unlock()
	_, ok := m.promptCancel[sessionID]
	return ok
}

func (m *Lifetime) SetScratch(folders *scratch.Folders) { m.scratch = folders }
func (m *Lifetime) SetProcesses(processes interface{ HasRunning(string) bool }) {
	m.processes = processes
}
