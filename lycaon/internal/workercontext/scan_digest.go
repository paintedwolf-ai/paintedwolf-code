package workercontext

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/scan"
)

var scanDrilldownTools = []string{"scan_summary", "scan_query"}

func attachWorkerScanDigest(ctx context.Context, out inject.WorkerLegContext, projectDir, workflowRunID string, list scan.WorkerScanLister) (inject.WorkerLegContext, error) {
	if list == nil || strings.TrimSpace(projectDir) == "" || !holdsAny(out.LegTools, scanDrilldownTools) {
		return out, nil
	}
	if workflowRunID = strings.TrimSpace(workflowRunID); workflowRunID != "" {
		lines, err := scan.WorkflowWorkerScanDigest(ctx, list, workflowRunID)
		if err != nil {
			return out, err
		}
		out.ScanDigest = lines
		return out, nil
	}
	out.ScanDigest = scan.WorkerScanDigest(ctx, list, projectDir, time.Now().UTC())
	return out, nil
}

func holdsAny(tools, want []string) bool {
	for _, t := range tools {
		for _, w := range want {
			if t == w {
				return true
			}
		}
	}
	return false
}
