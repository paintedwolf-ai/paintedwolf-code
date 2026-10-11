package workerworkspace

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

// maxReservationPromptRunes bounds the one-line brief shown beside a reservation.
const maxReservationPromptRunes = 48

// ReservationEntries returns handoff reservations for pack_board peer digest.
func (m *Service) ReservationEntries(sessionID string) []api.BoardReservationEntry {
	if m == nil || m.calls == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	rows, err := m.calls.ListActiveReservations(context.Background(), sessionID)
	if err != nil || len(rows) == 0 {
		return nil
	}
	out := make([]api.BoardReservationEntry, 0, len(rows))
	for _, row := range rows {
		path := strings.TrimSpace(row.Path)
		jobID := strings.TrimSpace(row.Agent)
		if path == "" || jobID == "" {
			continue
		}
		out = append(out, api.BoardReservationEntry{
			Path:     path,
			JobID:    jobID,
			LegLabel: m.legLabel(jobID),
		})
	}
	return out
}

// PeerReservations returns path holds by sibling workers for worker-leg inject.
func (m *Service) PeerReservations(ctx context.Context, childSessionID string) []inject.ReservedPath {
	if m == nil || m.calls == nil || m.store == nil {
		return nil
	}
	childSessionID = strings.TrimSpace(childSessionID)
	if childSessionID == "" {
		return nil
	}
	child, err := m.store.Get(ctx, childSessionID)
	if err != nil || child == nil {
		return nil
	}
	parentID := strings.TrimSpace(child.ParentSessionID)
	if parentID == "" {
		return nil
	}
	ownJob := ""
	if m.tasks != nil {
		if task, ok := m.tasks.Get(workercontext.Job(ctx)); ok && task != nil {
			ownJob = strings.TrimSpace(task.ID)
		}
	}
	rows, err := m.calls.ListActiveReservations(ctx, parentID)
	if err != nil || len(rows) == 0 {
		return nil
	}
	out := make([]inject.ReservedPath, 0, len(rows))
	for _, row := range rows {
		path := strings.TrimSpace(row.Path)
		jobID := strings.TrimSpace(row.Agent)
		if path == "" || jobID == "" || jobID == ownJob {
			continue
		}
		out = append(out, inject.ReservedPath{
			Path:     path,
			JobID:    jobID,
			LegLabel: m.legLabel(jobID),
		})
	}
	return out
}

func (m *Service) legLabel(jobID string) string {
	if m == nil || m.tasks == nil {
		return ""
	}
	task, ok := m.tasks.Get(strings.TrimSpace(jobID))
	if !ok || task == nil {
		return ""
	}
	if leg := strings.TrimSpace(task.LegID); leg != "" {
		return leg
	}
	prompt := strings.TrimSpace(task.Brief)
	if prompt == "" {
		return ""
	}
	if i := strings.IndexByte(prompt, '\n'); i >= 0 {
		prompt = strings.TrimSpace(prompt[:i])
	}
	return runeclamp.Clamp(prompt, maxReservationPromptRunes)
}
