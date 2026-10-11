package bgprocess

import "sort"

// ActiveExecutionJob retains exceptional access until an exit is observed.
type ActiveExecutionJob struct {
	Handle     string
	ToolCallID string
	Capability string
	Status     JobLiveness
}

func (r *Registry) ActiveExecutionJobs(sessionID string) []ActiveExecutionJob {
	if r == nil {
		return nil
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	var jobs []ActiveExecutionJob
	for _, process := range r.jobs.sessions[trim(sessionID)] {
		if process.hasExit {
			continue
		}
		capability := ""
		if process.boundary.ProcessControl {
			capability = "process_control"
		}
		if process.boundary.HostExecution {
			capability = "host_execution"
		}
		if capability == "" {
			continue
		}
		status := JobLivenessActive
		if process.livenessUnknown || !process.running {
			status = JobLivenessUnknown
		}
		jobs = append(jobs, ActiveExecutionJob{Handle: process.Handle, ToolCallID: process.toolCallID, Capability: capability, Status: status})
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Handle < jobs[j].Handle })
	return jobs
}
