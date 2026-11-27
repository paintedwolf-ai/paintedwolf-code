// Package call defines inter-agent file reservations (handoff_* reservation tools).
package call

import "context"

// ReservationResult is the outcome of file reservation.
type ReservationResult struct {
	Reserved []string
	Blocked  []string
}

// ActiveReservation is one held path on the parent session handoff plane.
type ActiveReservation struct {
	Path  string
	Agent string // job id
}

// CallManager manages handoff_reserve/release/release_all holds.
type CallManager interface {
	Reserve(ctx context.Context, sessionID string, paths []string, agent string) (*ReservationResult, error)
	Release(ctx context.Context, sessionID string, paths []string, agent string) error
	ReleaseAll(ctx context.Context, sessionID string, agent string) error
	ListActiveReservations(ctx context.Context, sessionID string) ([]ActiveReservation, error)
}
