package session

import "github.com/lycaon/lycaon/internal/progress"

// SetProgressStore wires the coordinator progress store for the plan-missing nudge.
func (m *Manager) SetProgressStore(store progress.RunScopedStore) {
	if m == nil {
		return
	}
	m.progress = store
	m.RewindRuntime.Progress = store
	m.Guards.SetProgress(store)
	m.Runner.PostTurn.SetProgress(store)
	m.Batch.SetProgress(store)
	m.Guidance.SetProgress(store)
	m.ProgressClosure.SetProgress(store)
	m.Stops.SetProgress(store)
	m.Workers.State.SetProgress(store)
	m.Runner.Instructions.SetProgress(store)
	m.Runner.History.SetProgress(store)
}
