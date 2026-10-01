package board

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// BuildWorkerRoster maps live worker jobs into slim pack_board rows.
func BuildWorkerRoster(tasks []api.WorkerTask, level api.BoardDetailLevel) []api.BoardWorkerRosterEntry {
	if level == api.BoardDetailLevelStatus {
		return nil
	}
	includeBrief := level == api.BoardDetailLevelFull
	out := make([]api.BoardWorkerRosterEntry, 0, len(tasks))
	for _, task := range tasks {
		agent := strings.TrimSpace(task.AgentType)
		if agent == "" {
			agent = "worker"
		}
		row := api.BoardWorkerRosterEntry{
			WorkerID:      task.ID,
			Dependencies:  task.Dependencies,
			AgentType:     agent,
			Status:        task.Status,
			MergeStatus:   task.MergeStatus,
			OverlayID:     strings.TrimSpace(task.OverlayID),
			ScopeSummary:  task.EffectiveScope().Summary(),
			Error:         strings.TrimSpace(task.Error),
			MaxToolLoops:  task.MaxToolLoops,
			BudgetRequest: task.BudgetRequest,
			ToolLoopsUsed: task.ToolLoopsUsed,
		}
		if paths := changedPathsFromTask(task); len(paths) > 0 {
			row.ChangedPaths = paths
		}
		if len(task.TouchedPaths) > 0 {
			row.TouchedPaths = append([]string(nil), task.TouchedPaths...)
		}
		if includeBrief {
			row.Brief = strings.TrimSpace(task.Brief)
		}
		out = append(out, row)
	}
	return out
}

func changedPathsFromTask(task api.WorkerTask) []string {
	if task.Result != nil && task.Result.ChangeReport != nil && len(task.Result.ChangeReport.ChangedPaths) > 0 {
		return append([]string(nil), task.Result.ChangeReport.ChangedPaths...)
	}
	return nil
}

// AttachRosterReservations maps active handoff_reserve paths onto roster rows by job_id.
func AttachRosterReservations(roster []api.BoardWorkerRosterEntry, reservations []api.BoardReservationEntry) []api.BoardWorkerRosterEntry {
	if len(roster) == 0 || len(reservations) == 0 {
		return roster
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
	if len(byJob) == 0 {
		return roster
	}
	out := make([]api.BoardWorkerRosterEntry, len(roster))
	for i, row := range roster {
		out[i] = row
		if paths := byJob[strings.TrimSpace(row.WorkerID)]; len(paths) > 0 {
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
