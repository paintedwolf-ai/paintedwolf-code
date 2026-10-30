package session

import "github.com/lycaon/lycaon/internal/progress"

// SetProgressStore connects the session progress owner.
func (m *Host) SetProgressStore(store progress.RunScopedStore) {
	if m == nil {
		return
	}
	m.RewindRuntime.Progress = store
	m.Coordinator.Guards.SetProgress(store)
	m.Runner.PostTurn.SetProgress(store)
	m.Coordinator.Batch.SetProgress(store)
	m.Coordinator.Guidance.SetProgress(store)
	m.Coordinator.ProgressClosure.SetProgress(store)
	m.Stops.SetProgress(store)
	m.Workers.State.SetProgress(store)
	m.Workers.State.SetProgressReconciliation(m.Coordinator.ProgressClosure)
	m.Runner.Instructions.SetProgress(store)
}
