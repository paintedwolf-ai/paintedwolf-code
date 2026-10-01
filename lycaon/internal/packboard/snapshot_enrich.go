package packboard

import "github.com/lycaon/lycaon/pkg/api"

// WorkerTasksFromSnapshot returns worker tasks embedded in a board snapshot.
func WorkerTasksFromSnapshot(snap api.BoardSnapshot) []api.WorkerTask {
	if snap.Workers == nil {
		return nil
	}
	raw, _ := (*snap.Workers)["tasks"].([]api.WorkerTask)
	return raw
}

// DelegationsFromSnapshot returns the delegations embedded in a board snapshot.
func DelegationsFromSnapshot(snap api.BoardSnapshot) []api.Delegation {
	if snap.Delegation == nil {
		return nil
	}
	raw, _ := (*snap.Delegation)["delegations"].([]api.Delegation)
	return raw
}

// PromotePathsFromSnapshot returns cached promote path_status rows from a board snapshot.
func PromotePathsFromSnapshot(snap api.BoardSnapshot) []api.WorkerPromoteJobPathStatus {
	if snap.Workers == nil {
		return nil
	}
	raw, _ := (*snap.Workers)["promote_paths"].([]api.WorkerPromoteJobPathStatus)
	return raw
}

// OverlayMergePlanFromSnapshot returns session overlay merge planning from a board snapshot.
func OverlayMergePlanFromSnapshot(snap api.BoardSnapshot) *api.OverlayMergePlan {
	if snap.Workers == nil {
		return nil
	}
	raw, _ := (*snap.Workers)["overlay_merge_plan"].(*api.OverlayMergePlan)
	return raw
}

// EnrichSnapshotPromotePaths attaches cached promote path_status rows to a board snapshot.
func EnrichSnapshotPromotePaths(snap *api.BoardSnapshot, rows []api.WorkerPromoteJobPathStatus) {
	if snap == nil || len(rows) == 0 {
		return
	}
	if snap.Workers == nil {
		snap.Workers = &api.BoardWorkersSlice{}
	}
	(*snap.Workers)["promote_paths"] = rows
}

// EnrichSnapshotOverlayMergePlan attaches session-level overlay merge planning for orientation lines.
func EnrichSnapshotOverlayMergePlan(snap *api.BoardSnapshot, plan *api.OverlayMergePlan) {
	if snap == nil || plan == nil || plan.PendingCount == 0 {
		return
	}
	if snap.Workers == nil {
		snap.Workers = &api.BoardWorkersSlice{}
	}
	(*snap.Workers)["overlay_merge_plan"] = plan
}

// EnrichSnapshotOverlay attaches cached promote path rows and overlay merge plan to a board snapshot.
func EnrichSnapshotOverlay(
	sessionID string,
	snap *api.BoardSnapshot,
	promoteFn func(string) []api.WorkerPromoteJobPathStatus,
	planFn func(string, []api.WorkerTask) *api.OverlayMergePlan,
) {
	if snap == nil {
		return
	}
	if promoteFn != nil {
		EnrichSnapshotPromotePaths(snap, promoteFn(sessionID))
	}
	if planFn != nil {
		tasks := WorkerTasksFromSnapshot(*snap)
		EnrichSnapshotOverlayMergePlan(snap, planFn(sessionID, tasks))
	}
}

// EnrichSnapshotReservationsForSession loads active handoff reservations onto a board snapshot.
func EnrichSnapshotReservationsForSession(
	sessionID string,
	snap *api.BoardSnapshot,
	listFn func(string) []api.BoardReservationEntry,
) {
	if snap == nil || listFn == nil {
		return
	}
	EnrichSnapshotReservations(snap, listFn(sessionID))
}
