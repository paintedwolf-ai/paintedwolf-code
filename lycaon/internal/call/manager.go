// Package call defines inter-agent file reservations (handoff_* reservation tools).
package call

import (
	"context"
	"time"
)

// Session holds call channel session state.
type Session struct {
	ID         string
	ProjectDir string
	CreatedAt  time.Time
}

// CallInitRequest starts a call session.
type CallInitRequest struct {
	SessionID  string
	ProjectDir string
	Agent      string
}

// ReservationResult is the outcome of file reservation.
type ReservationResult struct {
	Reserved []string
	Blocked  []string
}

// HealthStatus reports call subsystem health.
type HealthStatus struct {
	OK               bool
	ReservationCount int
}

// ActiveReservation is one held path on the parent session handoff plane.
type ActiveReservation struct {
	Path  string
	Agent string // job id
}

// CallManager manages handoff_init/reserve/release/health.
type CallManager interface {
	Init(ctx context.Context, req CallInitRequest) (*Session, error)
	Reserve(ctx context.Context, sessionID string, paths []string, agent string) (*ReservationResult, error)
	Release(ctx context.Context, sessionID string, paths []string, agent string) error
	ReleaseAll(ctx context.Context, sessionID string, agent string) error
	Health(ctx context.Context, sessionID string) (*HealthStatus, error)
	ListActiveReservations(ctx context.Context, sessionID string) ([]ActiveReservation, error)
}
