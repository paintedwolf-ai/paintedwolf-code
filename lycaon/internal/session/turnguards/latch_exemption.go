package turnguards

import (
	"context"
	"log/slog"
	"slices"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// LatestResultJobs names the job of a coordinator's worker's latest result,
// by worker name.
type LatestResultJobs func(ctx context.Context, sessionID, name string) (jobID string, ok bool, err error)

// ResultJobs names the job of one of a coordinator's worker results, by handle.
type ResultJobs func(ctx context.Context, sessionID, handle string) (jobID string, ok bool, err error)

// JobNames names the worker and result a job belongs to.
type JobNames interface {
	NamesByJob(ctx context.Context, jobID string) (workerrecords.JobNames, bool, error)
}

// SetJobNames sets how a job is named in closeout refusals.
func (m *Service) SetJobNames(names JobNames) { m.jobNames = names }

// SetWorkerResults sets how a worker name resolves to its latest result.
func (m *Service) SetWorkerResults(results LatestResultJobs) { m.latestResultJob = results }

// SetResultJobs sets how a result handle resolves to its job.
func (m *Service) SetResultJobs(results ResultJobs) { m.resultJob = results }

// accountsLatchedResult reports a call that accounts for a result the
// checklist latch holds, so the latch does not refuse it: an instruction to
// the worker that delivered it, or a review of it.
func (m *Service) accountsLatchedResult(ctx context.Context, sess *api.Session, tool string, args map[string]any, baseline guard.ProgressClosureBaseline) bool {
	if sess == nil {
		return false
	}
	var job string
	var ok bool
	var err error
	switch handle, _ := args["review_of"].(string); {
	case tool == "instruct_worker" && m.latestResultJob != nil:
		name, _ := args["worker"].(string)
		job, ok, err = m.latestResultJob(ctx, sess.ID, name)
	case tool == "task" && handle != "" && m.resultJob != nil:
		job, ok, err = m.resultJob(ctx, sess.ID, handle)
	default:
		return false
	}
	if err != nil {
		slog.WarnContext(ctx, "resolve the result a call accounts for", "session_id", sess.ID, "tool", tool, "error", err)
		return false
	}
	return ok && slices.Contains(baseline.JobIDs, job)
}
