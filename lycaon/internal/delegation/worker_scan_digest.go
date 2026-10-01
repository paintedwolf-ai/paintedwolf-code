package delegation

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/scan"
)

// scanDrilldownTools are the tools a scan digest points a worker at.
var scanDrilldownTools = []string{"scan_summary", "scan_query"}

// attachWorkerScanDigest gives a worker that can read scans its run's bound
// scans, or the project's latest scan when its job belongs to no run.
func attachWorkerScanDigest(out inject.WorkerLegContext, projectDir, workflowRunID string, list scan.WorkerScanLister) (inject.WorkerLegContext, error) {
	if list == nil || strings.TrimSpace(projectDir) == "" || !holdsAny(out.LegTools, scanDrilldownTools) {
		return out, nil
	}
	if workflowRunID = strings.TrimSpace(workflowRunID); workflowRunID != "" {
		lines, err := scan.WorkflowWorkerScanDigest(context.Background(), list, workflowRunID)
		if err != nil {
			return out, err
		}
		out.ScanDigest = lines
		return out, nil
	}
	out.ScanDigest = scan.WorkerScanDigest(context.Background(), list, projectDir, time.Now().UTC())
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
