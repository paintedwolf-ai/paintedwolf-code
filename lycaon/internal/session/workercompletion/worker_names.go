package workercompletion

import (
	"context"
	"log/slog"
	"strings"
)

// WorkerNames names the worker whose round a job runs. ok is false for a job
// that runs no worker round.
type WorkerNames func(ctx context.Context, jobID string) (name string, ok bool, err error)

// Name returns the job's worker name, or "" when there is none or it cannot
// be read.
func (n WorkerNames) Name(ctx context.Context, jobID string) string {
	if n == nil || strings.TrimSpace(jobID) == "" {
		return ""
	}
	name, ok, err := n(ctx, jobID)
	if err != nil {
		slog.WarnContext(ctx, "name the worker of a job", "job_id", jobID, "error", err)
		return ""
	}
	if !ok {
		return ""
	}
	return name
}
