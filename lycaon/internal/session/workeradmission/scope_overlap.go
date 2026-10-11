package workeradmission

import (
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	TaskScopeWriteRequiredCode   = "TASK_SCOPE_WRITE_REQUIRED"
	TaskScopeProfileReadOnlyCode = "TASK_SCOPE_PROFILE_READ_ONLY"
	TaskMaxToolLoopsInvalidCode  = "TASK_MAX_TOOL_LOOPS_INVALID"
	RepoEmptyReadOnlyWorkerCode  = "REPO_EMPTY_READ_ONLY_WORKER"
)

// ParallelTaskCaps holds total and read/write in-flight limits for task().
type ParallelTaskCaps struct {
	MaxTotal int
	MaxRead  int
	MaxWrite int
}

// ResolveParallelTaskCaps applies host defaults when manifest caps are unset.
func ResolveParallelTaskCaps(maxTotal, maxRead, maxWrite int) ParallelTaskCaps {
	total := CoordinatorTaskConcurrencyCap(maxTotal)
	read := maxRead
	if read <= 0 {
		read = spawn.DefaultMaxReadTaskWorkers
	}
	write := maxWrite
	if write <= 0 {
		write = spawn.DefaultMaxWriteTaskWorkers
	}
	return ParallelTaskCaps{MaxTotal: total, MaxRead: read, MaxWrite: write}
}

// ValidateTaskScopeForProfile keeps task mode aligned with the tool profile.
func ValidateTaskScopeForProfile(mutationCapable, known bool, scope api.TaskScope) string {
	if !known {
		return ""
	}
	write := scope.Normalized().IsWrite()
	if mutationCapable && !write {
		return TaskScopeWriteRequiredCode
	}
	if !mutationCapable && write {
		return TaskScopeProfileReadOnlyCode
	}
	return ""
}

// ValidateTaskScopeForAgent checks task mode against the agent profile.
func ValidateTaskScopeForAgent(agentType string, scope api.TaskScope) string {
	capable, known := prompts.AgentMutationCapable(agentType)
	return ValidateTaskScopeForProfile(capable, known, scope)
}

// CountInFlightByMode counts read and write scopes among active jobs.
func CountInFlightByMode(active []api.WorkerTask) (reads, writes int) {
	for _, job := range active {
		if job.EffectiveScope().IsWrite() {
			writes++
		} else {
			reads++
		}
	}
	return reads, writes
}

// TaskScopeCapExceeded returns true when the new scope would exceed read/write caps.
func TaskScopeCapExceeded(active []api.WorkerTask, scope api.TaskScope, caps ParallelTaskCaps) bool {
	reads, writes := CountInFlightByMode(active)
	scope = scope.Normalized()
	if scope.IsWrite() {
		return writes >= caps.MaxWrite
	}
	return reads >= caps.MaxRead
}
