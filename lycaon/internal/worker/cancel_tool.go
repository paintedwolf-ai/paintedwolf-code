package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

// CancelToolDeps wires the coordinator worker_cancel tool.
type CancelToolDeps struct {
	Cancel *CancelService
}

// RegisterWorkerCancelTool registers worker_cancel(job_id, reason?) for coordinator sessions.
func RegisterWorkerCancelTool(reg *tools.DefaultRegistry, deps CancelToolDeps) error {
	if reg == nil || deps.Cancel == nil {
		return fmt.Errorf("registry and cancel service required")
	}
	return reg.Register("worker_cancel", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		jobID, _ := args["job_id"].(string)
		jobID = strings.TrimSpace(jobID)
		if jobID == "" {
			return "", fmt.Errorf("job_id is required")
		}
		captureWorkerSubject(tctx, deps.Cancel.Queue, jobID)
		reason, _ := args["reason"].(string)
		out, err := deps.Cancel.CancelForSession(ctx, tctx.Identity.SessionID, jobID, reason)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	})
}
