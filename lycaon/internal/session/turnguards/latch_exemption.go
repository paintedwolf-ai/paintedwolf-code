package turnguards

import (
	"context"
	"log/slog"
	"slices"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/pkg/api"
)

// LatestResultJobs names the job of a coordinator's worker's latest result,
// by worker name.
type LatestResultJobs func(ctx context.Context, sessionID, name string) (jobID string, ok bool, err error)

// SetWorkerResults sets how a worker name resolves to its latest result.
func (m *Service) SetWorkerResults(results LatestResultJobs) { m.latestResultJob = results }

// instructsLatchedWorker reports an instruction to the worker whose latest
// result the checklist latch holds. The instruction accounts for that result,
// so the latch does not refuse it.
func (m *Service) instructsLatchedWorker(ctx context.Context, sess *api.Session, tool string, args map[string]any, baseline guard.ProgressClosureBaseline) bool {
	if tool != "instruct_worker" || m.latestResultJob == nil || sess == nil {
		return false
	}
	name, _ := args["worker"].(string)
	job, ok, err := m.latestResultJob(ctx, sess.ID, name)
	if err != nil {
		slog.WarnContext(ctx, "resolve instructed worker for the checklist latch", "session_id", sess.ID, "error", err)
		return false
	}
	return ok && slices.Contains(baseline.JobIDs, job)
}
