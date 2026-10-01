package call

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// SQLManager implements CallManager against SQLite call_* tables.
type SQLManager struct {
	store *SQLStore
}

// NewSQLManager constructs a CallManager backed by call_* tables.
func NewSQLManager(database db.Handle, sessions SessionLookup) *SQLManager {
	return &SQLManager{store: NewSQLStore(database, sessions)}
}

// Init verifies the handoff session exists and returns channel metadata.
func (m *SQLManager) Init(ctx context.Context, req CallInitRequest) (*Session, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return nil, ErrSessionNotFound
	}
	projectDir, err := m.store.projectDir(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	_ = strings.TrimSpace(req.Agent)
	return &Session{
		ID:         sessionID,
		ProjectDir: projectDir,
		CreatedAt:  time.Now().UTC(),
	}, nil
}

// Reserve records path locks for an agent on a session plane.
func (m *SQLManager) Reserve(ctx context.Context, sessionID string, paths []string, agent string) (*ReservationResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	agent = strings.TrimSpace(agent)
	if sessionID == "" || agent == "" {
		return nil, ErrSessionNotFound
	}
	projectDir, err := m.store.projectDir(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := &ReservationResult{}
	for _, raw := range paths {
		path, err := NormalizeRelativePath(raw)
		if err != nil {
			return nil, err
		}
		holder, err := m.store.ReservationHolder(ctx, projectDir, path, agent)
		if err != nil {
			return nil, err
		}
		if holder != "" {
			out.Blocked = append(out.Blocked, path)
			continue
		}
		if err := m.store.InsertReservation(ctx, sessionID, agent, path); err != nil {
			return nil, err
		}
		out.Reserved = append(out.Reserved, path)
	}
	return out, nil
}

// Release deletes one or more path reservations for an agent.
func (m *SQLManager) Release(ctx context.Context, sessionID string, paths []string, agent string) error {
	sessionID = strings.TrimSpace(sessionID)
	agent = strings.TrimSpace(agent)
	if sessionID == "" || agent == "" {
		return ErrSessionNotFound
	}
	if _, err := m.store.projectDir(ctx, sessionID); err != nil {
		return err
	}
	for _, raw := range paths {
		path, err := NormalizeRelativePath(raw)
		if err != nil {
			return err
		}
		if err := m.store.DeleteReservation(ctx, sessionID, agent, path); err != nil {
			return err
		}
	}
	return nil
}

// ReleaseAll clears every reservation held by an agent on a session.
func (m *SQLManager) ReleaseAll(ctx context.Context, sessionID string, agent string) error {
	sessionID = strings.TrimSpace(sessionID)
	agent = strings.TrimSpace(agent)
	if sessionID == "" || agent == "" {
		return nil
	}
	return m.store.DeleteAllReservationsForAgent(ctx, sessionID, agent)
}

// Health reports reservation counts for a session.
func (m *SQLManager) Health(ctx context.Context, sessionID string) (*HealthStatus, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return &HealthStatus{}, nil
	}
	if _, err := m.store.projectDir(ctx, sessionID); err != nil {
		return nil, err
	}
	resCount, err := m.store.CountReservations(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &HealthStatus{
		OK:               true,
		ReservationCount: resCount,
	}, nil
}

// ListActiveReservations returns every path reservation on the session plane.
func (m *SQLManager) ListActiveReservations(ctx context.Context, sessionID string) ([]ActiveReservation, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	if _, err := m.store.projectDir(ctx, sessionID); err != nil {
		return nil, err
	}
	return m.store.ListActiveReservations(ctx, sessionID)
}
