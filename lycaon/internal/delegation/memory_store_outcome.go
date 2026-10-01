package delegation

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

func (s *MemoryStore) RecordLegOutcome(ctx context.Context, leg api.Leg) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.delegations[leg.DelegationID]
	if !ok {
		return false, ErrDelegationNotFound
	}
	if delegationSettled(&rec.delegation) {
		return false, nil
	}

	current, ok := rec.legs[leg.ID]
	if !ok {
		return false, ErrLegNotFound
	}
	if current.WorkerID != leg.WorkerID || current.WorkerID == "" || current.Status == leg.Status {
		return false, nil
	}
	switch current.Status {
	case api.LegStatusDispatched, api.LegStatusRunning, api.LegStatusHeld:
	default:
		return false, nil
	}
	current.Status = leg.Status
	current.Result = leg.Result
	current.CompletedAt = leg.CompletedAt
	return true, nil
}
