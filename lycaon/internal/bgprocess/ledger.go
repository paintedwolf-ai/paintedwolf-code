package bgprocess

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/hostcmd"
)

// LedgerJobs is the session's command-job ledger: every live job, and every
// retained job that exited on its own before the agent observed it. Reading
// the ledger changes nothing.
func (r *Registry) LedgerJobs(sessionID string) []JobSnapshot {
	if r == nil {
		return nil
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	var out []JobSnapshot
	for _, proc := range r.jobs.sessions[strings.TrimSpace(sessionID)] {
		if (proc.kind != processKindPipeline && proc.kind != processKindProgram) || proc.silent {
			continue
		}
		exited := proc.hasExit && proc.reason == TerminationExited && !proc.observed
		if !proc.running && !exited {
			continue
		}
		job := JobSnapshot{
			Handle: proc.Handle, Mode: proc.mode, OriginTool: proc.originTool,
			StartedAt: proc.startedAt, Timeout: proc.timeout,
			Stages: append([]hostcmd.StageResult(nil), proc.Stages...),
		}
		if exited {
			refusals := proc.confinementLocked().Refusals()
			job.Exited, job.ExitCode, job.Refusals = true, proc.exitCode, len(refusals.Refusals)+refusals.Omitted
		}
		out = append(out, job)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// Observe records delivery of a job's terminal state.
func (r *Registry) Observe(sessionID, handle string) {
	if r == nil {
		return
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	proc := r.jobs.sessions[strings.TrimSpace(sessionID)][strings.TrimSpace(handle)]
	if proc != nil && proc.hasExit {
		proc.observed = true
	}
}

// Observed reports whether the agent has received this job's terminal state.
func (r *Registry) Observed(sessionID, handle string) bool {
	if r == nil {
		return false
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	proc := r.jobs.sessions[strings.TrimSpace(sessionID)][strings.TrimSpace(handle)]
	return proc != nil && proc.observed
}
