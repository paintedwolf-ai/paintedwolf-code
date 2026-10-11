package bgprocess

import (
	"sort"

	"github.com/lycaon/lycaon/internal/confine"
)

// ActiveDirectIPJob is one exceptional background/promoted pipeline under NetworkDirectIP.
type ActiveDirectIPJob struct {
	Handle     string
	ToolCallID string
	Status     JobLiveness
}

// ActiveDirectIPJobs includes running and liveness-unknown direct-IP jobs without an observed exit.
func (r *Registry) ActiveDirectIPJobs(sessionID string) []ActiveDirectIPJob {
	if r == nil {
		return nil
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	var out []ActiveDirectIPJob
	for _, proc := range r.jobs.sessions[trim(sessionID)] {
		status, ok := proc.directIPProtectionStatusLocked()
		if !ok {
			continue
		}
		out = append(out, ActiveDirectIPJob{
			Handle:     proc.Handle,
			ToolCallID: proc.toolCallID,
			Status:     status,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out
}

func (proc *Process) directIPProtectionStatusLocked() (JobLiveness, bool) {
	if proc == nil || proc.boundary.HostExecution || proc.boundary.Network != confine.NetworkDirectIP || proc.kind != processKindPipeline {
		return "", false
	}
	if proc.hasExit {
		return "", false
	}
	if proc.livenessUnknown || !proc.running {
		return JobLivenessUnknown, true
	}
	return JobLivenessActive, true
}

// MarkDirectIPLivenessUnknown records uncertain liveness without assigning an exit state.
func (r *Registry) MarkDirectIPLivenessUnknown(sessionID, handle string) error {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return err
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	if proc.boundary.Network != confine.NetworkDirectIP || proc.kind != processKindPipeline {
		return ErrProcessNotFound
	}
	if proc.hasExit {
		return ErrProcessNotRunning
	}
	proc.livenessUnknown = true
	return nil
}
