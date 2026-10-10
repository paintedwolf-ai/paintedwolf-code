// Package lifecycle serializes session admission and stop transitions across a session tree.
package lifecycle

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/session/tree"
)

// ErrStopping rejects work after session stop begins.
var ErrStopping = errors.New("session is stopping")

type Flight struct {
	done chan struct{}
	err  error
}

type Turn struct {
	root       string
	generation uint64
}

type State struct {
	store       tree.Reader
	mu          sync.Mutex
	generations map[string]uint64
	active      map[string]*Flight
	gates       map[string]*admissionGate
}

func New(store tree.Reader) *State { return &State{store: store} }

func (m *State) RootID(ctx context.Context, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || m == nil || m.store == nil {
		return sessionID
	}
	if root := strings.TrimSpace(tree.RootID(ctx, m.store, sessionID)); root != "" {
		return root
	}
	return sessionID
}

type admissionGate struct {
	mu   sync.RWMutex
	refs int
}

func (m *State) Begin(rootID string) (*Flight, bool) {
	rootID = strings.TrimSpace(rootID)
	gate := m.retainGate(rootID)
	defer m.releaseGate(rootID, gate)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return m.beginUnderGate(rootID)
}

func (m *State) beginUnderGate(rootID string) (*Flight, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		m.active = make(map[string]*Flight)
	}
	if flight := m.active[rootID]; flight != nil {
		return flight, false
	}
	if m.generations == nil {
		m.generations = make(map[string]uint64)
	}
	m.generations[rootID]++
	flight := &Flight{done: make(chan struct{})}
	m.active[rootID] = flight
	return flight, true
}

func (m *State) Finish(rootID string, flight *Flight, err error) {
	m.mu.Lock()
	flight.err = err
	if m.active[rootID] == flight {
		delete(m.active, rootID)
	}
	close(flight.done)
	m.mu.Unlock()
}

func (m *State) Capture(ctx context.Context, sessionID string) (Turn, error) {
	rootID := m.RootID(ctx, sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active[rootID] != nil {
		return Turn{}, ErrStopping
	}
	return Turn{root: rootID, generation: m.generations[rootID]}, nil
}

func (m *State) MayDrain(token Turn) bool {
	if m == nil || token.root == "" {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[token.root] == nil && m.generations[token.root] == token.generation
}

func (m *State) InProgress(ctx context.Context, sessionID string) bool {
	rootID := m.RootID(ctx, sessionID)
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[rootID] != nil
}

// WithSessionTreeAdmission serializes new work against session stop.
func (m *State) WithSessionTreeAdmission(ctx context.Context, sessionID string, fn func() error) error {
	rootID := m.RootID(ctx, sessionID)
	gate := m.retainGate(rootID)
	defer m.releaseGate(rootID, gate)
	gate.mu.RLock()
	defer gate.mu.RUnlock()
	m.mu.Lock()
	if m.active[rootID] != nil {
		m.mu.Unlock()
		return ErrStopping
	}
	m.mu.Unlock()
	return fn()
}

func (m *State) Forget(sessionID string) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	m.mu.Lock()
	if m.active[sessionID] == nil {
		delete(m.generations, sessionID)
		if gate := m.gates[sessionID]; gate != nil && gate.refs == 0 {
			delete(m.gates, sessionID)
		}
	}
	m.mu.Unlock()
}

func (m *State) retainGate(rootID string) *admissionGate {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gates == nil {
		m.gates = make(map[string]*admissionGate)
	}
	gate := m.gates[rootID]
	if gate == nil {
		gate = &admissionGate{}
		m.gates[rootID] = gate
	}
	gate.refs++
	return gate
}

func (m *State) releaseGate(rootID string, gate *admissionGate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	gate.refs--
	if gate.refs == 0 && m.active[rootID] == nil {
		if _, live := m.generations[rootID]; !live && m.gates[rootID] == gate {
			delete(m.gates, rootID)
		}
	}
}

func (m *State) Commit(ctx context.Context, rootID string, transition func(context.Context) error) (*Flight, error) {
	gate := m.retainGate(rootID)
	defer m.releaseGate(rootID, gate)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if m.InProgress(ctx, rootID) {
		return nil, ErrStopping
	}
	// Only a committed decision advances the turn generation.
	if err := transition(ctx); err != nil {
		return nil, err
	}
	flight, _ := m.beginUnderGate(rootID)
	return flight, nil
}

func (f *Flight) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.done:
		return f.err
	}
}
