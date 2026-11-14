package bgprocess

import (
	"sort"

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
	for _, proc := range r.jobs.sessions[trim(sessionID)] {
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

// Observe records that the agent saw the job's state: it read the job's
// output, waited on it, or a result reported its exit.
func (r *Registry) Observe(sessionID, handle string) {
	if r == nil {
		return
	}
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return
	}
	r.jobs.mu.Lock()
	proc.observed = true
	r.jobs.mu.Unlock()
}
