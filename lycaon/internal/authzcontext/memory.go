package authzcontext

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// MemoryStore is an in-memory ContextStore for unit tests.
type MemoryStore struct {
	mu     sync.Mutex
	rows   map[string][]Context
	events map[string][]Event
}

// NewMemoryStore returns an empty memory-backed store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: make(map[string][]Context)}
}

func (m *MemoryStore) AppendContext(ctx context.Context, c Context) (bool, error) {
	if m == nil {
		return false, ErrNilStore
	}
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := m.rows[c.SessionID]
	if len(chain) > 0 {
		last := chain[len(chain)-1]
		if last.ConfigHash == c.ConfigHash {
			return false, nil
		}
		c.ContextSeq = last.ContextSeq + 1
		c.PrevHash = last.RowHash
	} else {
		c.ContextSeq = 1
		c.PrevHash = ""
	}
	if c.HashVersion == 0 {
		c.HashVersion = HashVersion1
	}
	payload := chainPayloadFromContext(c)
	var err error
	c.RowHash, err = ComputeRowHash(c.HashVersion, c.SessionID, c.ContextSeq, chainTimestamp(c), payload, c.PrevHash)
	if err != nil {
		return false, err
	}
	m.rows[c.SessionID] = append(chain, c)
	return true, nil
}

func (m *MemoryStore) LatestContext(ctx context.Context, sessionID string) (*Context, error) {
	rows, err := m.ListContexts(ctx, sessionID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	last := rows[len(rows)-1]
	return &last, nil
}

func (m *MemoryStore) ListContexts(ctx context.Context, sessionID string) ([]Context, error) {
	if m == nil {
		return nil, nil
	}
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := m.rows[sessionID]
	out := append([]Context(nil), chain...)
	return out, nil
}

func (m *MemoryStore) AppendEvent(ctx context.Context, e Event) error {
	if m == nil {
		return ErrNilStore
	}
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.events == nil {
		m.events = make(map[string][]Event)
	}
	chain := m.events[e.SessionID]
	if len(chain) > 0 {
		last := chain[len(chain)-1]
		e.EventSeq = last.EventSeq + 1
		e.PrevHash = last.RowHash
	} else {
		e.EventSeq = 1
		e.PrevHash = ""
	}
	if e.HashVersion == 0 {
		e.HashVersion = HashVersion1
	}
	payload := eventChainPayloadFromEvent(e)
	var err error
	e.RowHash, err = ComputeEventRowHash(e.HashVersion, e.SessionID, e.EventSeq, eventChainTimestamp(e), payload, e.PrevHash)
	if err != nil {
		return err
	}
	m.events[e.SessionID] = append(chain, e)
	return nil
}

func (m *MemoryStore) AppendEventTx(context.Context, *sql.Tx, Event) error {
	return fmt.Errorf("authzcontext: transactional memory append unavailable")
}

func (m *MemoryStore) LatestContextTx(ctx context.Context, _ *sql.Tx, sessionID string) (*Context, error) {
	return m.LatestContext(ctx, sessionID)
}

func (m *MemoryStore) ListEvents(ctx context.Context, sessionID string) ([]Event, error) {
	if m == nil {
		return nil, nil
	}
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.events == nil {
		return nil, nil
	}
	chain := m.events[sessionID]
	out := append([]Event(nil), chain...)
	return out, nil
}

// EventCount returns the number of recorded events for a session (tests).
func (m *MemoryStore) EventCount(sessionID string) int {
	if m == nil || m.events == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events[sessionID])
}

// FailStore always returns an error on append (for fail-closed tests).
type FailStore struct{}

func (FailStore) AppendContext(context.Context, Context) (bool, error) {
	return false, fmt.Errorf("authzcontext: injected store failure")
}

func (FailStore) LatestContext(context.Context, string) (*Context, error) {
	return nil, nil
}

func (FailStore) ListContexts(context.Context, string) ([]Context, error) {
	return nil, nil
}

func (FailStore) AppendEvent(context.Context, Event) error {
	return fmt.Errorf("authzcontext: injected store failure")
}

func (FailStore) AppendEventTx(context.Context, *sql.Tx, Event) error {
	return fmt.Errorf("authzcontext: injected store failure")
}

func (FailStore) LatestContextTx(context.Context, *sql.Tx, string) (*Context, error) {
	return nil, nil
}

func (FailStore) ListEvents(context.Context, string) ([]Event, error) {
	return nil, nil
}
