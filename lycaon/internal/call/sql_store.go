package call

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// SessionLookup resolves session rows for project binding.
type SessionLookup interface {
	ProjectDir(ctx context.Context, sessionID string) (string, error)
}

// SQLStore persists call reservations.
type SQLStore struct {
	Sessions SessionLookup

	queries *db.Queries
}

// NewSQLStore constructs a reservation store bound to database.
func NewSQLStore(database db.Handle, sessions SessionLookup) *SQLStore {
	return &SQLStore{Sessions: sessions, queries: db.New(database)}
}

func (s *SQLStore) projectDir(ctx context.Context, sessionID string) (string, error) {
	if s == nil || s.Sessions == nil {
		return "", ErrSessionNotFound
	}
	dir, err := s.Sessions.ProjectDir(ctx, sessionID)
	if err != nil {
		return "", err
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", ErrSessionNotFound
	}
	return dir, nil
}

// InsertReservation records a path reservation for an agent on a session.
func (s *SQLStore) InsertReservation(ctx context.Context, sessionID, agent, path string) error {
	return s.queries.InsertCallReservation(ctx, db.InsertCallReservationParams{
		Path:      path,
		Agent:     agent,
		SessionID: db.NullString(sessionID),
		CreatedAt: db.FormatTime(time.Now().UTC()),
	})
}

// ReservationHolder returns the agent holding path in projectDir, if any.
func (s *SQLStore) ReservationHolder(ctx context.Context, projectDir, path, exceptAgent string) (string, error) {
	holder, err := s.queries.GetCallReservationHolder(ctx, db.GetCallReservationHolderParams{
		Path:        path,
		ExceptAgent: exceptAgent,
		ProjectDir:  projectDir,
	})
	if db.IsNoRows(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(holder), nil
}

// DeleteReservation removes one reservation row.
func (s *SQLStore) DeleteReservation(ctx context.Context, sessionID, agent, path string) error {
	return s.queries.DeleteCallReservation(ctx, db.DeleteCallReservationParams{
		SessionID: db.NullString(sessionID),
		Agent:     agent,
		Path:      path,
	})
}

// DeleteAllReservationsForAgent clears every reservation for an agent on a session.
func (s *SQLStore) DeleteAllReservationsForAgent(ctx context.Context, sessionID, agent string) error {
	return s.queries.DeleteAgentCallReservations(ctx, db.DeleteAgentCallReservationsParams{
		SessionID: db.NullString(sessionID),
		Agent:     agent,
	})
}

// CountReservations returns reservation rows for a session.
func (s *SQLStore) CountReservations(ctx context.Context, sessionID string) (int, error) {
	n, err := s.queries.CountCallReservations(ctx, db.NullString(sessionID))
	return int(n), err
}

// ListActiveReservations returns path holds for a session ordered by path.
func (s *SQLStore) ListActiveReservations(ctx context.Context, sessionID string) ([]ActiveReservation, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	rows, err := s.queries.ListActiveCallReservations(ctx, db.NullString(sessionID))
	if err != nil {
		return nil, err
	}
	var out []ActiveReservation
	for _, r := range rows {
		path := strings.TrimSpace(r.Path)
		agent := strings.TrimSpace(r.Agent)
		if path == "" || agent == "" {
			continue
		}
		out = append(out, ActiveReservation{Path: path, Agent: agent})
	}
	return out, nil
}
