package workeradmission

import "github.com/lycaon/lycaon/internal/spawn"

const CoordinatorWorkerInFlightCode = "COORDINATOR_WORKER_IN_FLIGHT"

func CoordinatorTaskConcurrencyCap(maxWorkers int) int {
	if maxWorkers > 0 {
		return maxWorkers
	}
	return spawn.MaxInFlightTaskWorkers
}
