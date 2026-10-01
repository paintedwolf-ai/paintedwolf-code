package scan

import (
	"context"
	"fmt"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkflowAdvisoryInventory supplies run-bound scanner data independently of
// coordinator-selected claims. The query cursor exposes the rest of a large set.
func WorkflowAdvisoryInventory(ctx context.Context, ledger WorkflowRunScanLister, runID string) (string, error) {
	if ledger == nil {
		return "", fmt.Errorf("scan inventory unavailable")
	}
	scans, err := ledger.ListByWorkflowRunID(ctx, runID)
	if err != nil {
		return "", err
	}
	var ids []string
	var findings []api.SecurityFinding
	for _, s := range scans {
		if s.Status != api.CodeScanStatusComplete {
			continue
		}
		ids = append(ids, s.ID)
		for _, f := range s.Findings {
			if scanfindings.FindingKind(f) == api.FindingKindSCA {
				findings = append(findings, f)
			}
		}
	}
	groups := scanfindings.GroupFindings(findings, 3)
	total := len(groups)
	var next *int
	if total > 50 {
		offset := 50
		next = &offset
		groups = groups[:50]
	}
	data, err := surveyjson.Marshal(struct {
		ScanIDs     []string                    `json:"scan_ids"`
		Kind        string                      `json:"kind"`
		View        string                      `json:"view"`
		Groups      []scanfindings.FindingGroup `json:"groups"`
		TotalGroups int                         `json:"total_groups"`
		NextOffset  *int                        `json:"next_offset,omitempty"`
	}{ids, "sca", "groups", groups, total, next})
	if err != nil {
		return "", err
	}
	return string(data), nil
}
