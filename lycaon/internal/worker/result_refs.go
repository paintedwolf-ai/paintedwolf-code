package worker

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"

	"github.com/lycaon/lycaon/internal/workerrecords"
)

// JobNamer names the worker and result a job belongs to.
type JobNamer interface {
	NamesByJob(ctx context.Context, jobID string) (workerrecords.JobNames, bool, error)
}

// withResultRef adds the worker and result handle of the refusal's job, the
// names a coordinator addresses them by.
func withResultRef(ctx context.Context, refs JobNamer, data map[string]any) map[string]any {
	if refs == nil || data == nil {
		return data
	}
	jobID, _ := data["job_id"].(string)
	if strings.TrimSpace(jobID) == "" {
		return data
	}
	ref, ok, err := refs.NamesByJob(ctx, jobID)
	if err != nil {
		slog.WarnContext(ctx, "name the worker of a refused job", "job_id", jobID, "error", err)
		return data
	}
	if !ok {
		return data
	}
	data["worker"] = ref.Worker
	if ref.Handle != "" {
		data["overlay_id"] = ref.Handle
	}
	return data
}

// overlayHandles maps jobs to their result handles; a job without a settled
// worker result has none and is left out.
func overlayHandles(ctx context.Context, refs JobNamer, jobIDs []string) []string {
	if refs == nil {
		return nil
	}
	var out []string
	for _, id := range jobIDs {
		names, ok, err := refs.NamesByJob(ctx, id)
		if err != nil {
			slog.WarnContext(ctx, "name an overlapping job", "job_id", id, "error", err)
			continue
		}
		if ok && names.Handle != "" {
			out = append(out, names.Handle)
		}
	}
	return out
}

// nameOverlaps gives a merge result the result handles of its overlapping
// siblings.
func (s *MergeService) nameOverlaps(ctx context.Context, out *api.WorkerMergeResult) {
	if s == nil || out == nil {
		return
	}
	out.OverlapOverlayIDs = overlayHandles(ctx, s.Names, out.OverlapJobIDs)
}
