package board

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// attachWorkerReservations gives each worker the handoff_reserve paths its
// live round holds.
func attachWorkerReservations(workers []api.BoardWorker, reservations []api.BoardReservationEntry) []api.BoardWorker {
	if len(workers) == 0 || len(reservations) == 0 {
		return workers
	}
	byJob := make(map[string][]string)
	for _, row := range reservations {
		jobID := strings.TrimSpace(row.JobID)
		path := strings.TrimSpace(row.Path)
		if jobID == "" || path == "" {
			continue
		}
		byJob[jobID] = append(byJob[jobID], path)
	}
	out := make([]api.BoardWorker, len(workers))
	for i, w := range workers {
		out[i] = w
		if paths := byJob[strings.TrimSpace(w.LatestJobID)]; len(paths) > 0 {
			out[i].Reservations = append([]string(nil), paths...)
		}
	}
	return out
}

// BuildDelegationRoster maps delegations into slim pack_board rows.
func BuildDelegationRoster(delegations []api.Delegation, level api.BoardDetailLevel) []api.BoardDelegationEntry {
	if level == api.BoardDetailLevelStatus {
		return nil
	}
	out := make([]api.BoardDelegationEntry, 0, len(delegations))
	for _, d := range delegations {
		if d.Phase == api.DelegationPhaseDone {
			continue
		}
		out = append(out, api.BoardDelegationEntry{
			ID:    d.ID,
			Phase: d.Phase,
			Task:  strings.TrimSpace(d.Task),
		})
	}
	return out
}
