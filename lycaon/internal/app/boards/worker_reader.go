package boards

import "github.com/lycaon/lycaon/internal/workerops"

// WorkerReader reads this runtime's workers.
func (r *Runtime) WorkerReader() workerops.Reader {
	return workerops.NewReader(r.WorkerRecords, r.deps.Workers.Queue, r.deps.Sessions.Decisions, r.deps.Sessions.Checkpoints)
}
