package session

import "github.com/lycaon/lycaon/internal/progress"

// SetProgressStore wires the coordinator progress store for the plan-missing nudge.
func (m *Host) SetProgressStore(store progress.RunScopedStore) {
	if m == nil {
		return
	}
	m.RewindRuntime.Progress = store
	m.RewindRuntime.Progress = store
	m.Coordinator.Guards.SetProgress(store)
	m.Runner.PostTurn.SetProgress(store)
	m.Coordinator.Batch.SetProgress(store)
	m.Coordinator.Guidance.SetProgress(store)
	m.Coordinator.ProgressClosure.SetProgress(store)
	m.Stops.SetProgress(store)
	m.Workers.State.SetProgress(store)
	m.Runner.Instructions.SetProgress(store)
	m.Runner.History.SetProgress(store)
}
