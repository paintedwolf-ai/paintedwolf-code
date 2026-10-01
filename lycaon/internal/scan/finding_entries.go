package scan

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

func loadFindingEntries(ctx context.Context, q *db.Queries, setID string) ([]api.SecurityFinding, error) {
	rows, err := q.FindingEntries(ctx, setID)
	if err != nil {
		return nil, err
	}
	findings := make([]api.SecurityFinding, 0, len(rows))
	for _, row := range rows {
		var finding api.SecurityFinding
		if err := json.Unmarshal([]byte(row), &finding); err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

func publishScanSummary(ctx context.Context, q *db.Queries, scan *api.CodeScan) error {
	summary := ApplyScanView(scan, "summary")
	if summary.FindingSetID != "" {
		raw, err := q.GetFindingRollup(ctx, summary.FindingSetID)
		if err != nil {
			return err
		}
		var rollup findingRollup
		if err := json.Unmarshal([]byte(raw), &rollup); err != nil {
			return err
		}
		summary.FindingsCount = rollup.Count
		summary.FindingsStored = rollup.Count
		summary.FindingsByLevel = rollup.ByLevel
		summary.FindingsByKind = rollup.ByKind
		summary.UnmappedCount = rollup.Unmapped
		summary.TopLocations = mergeFindingRollups([]findingRollup{rollup}).TopLocations
	}
	raw, err := surveyjson.Marshal(summary)
	if err != nil {
		return err
	}
	list := *summary
	list.Guidance = nil
	list.TargetPaths = nil
	list.DeletedPaths = nil
	listRaw, err := surveyjson.Marshal(list)
	if err != nil {
		return err
	}
	return q.PutScanSummary(ctx, db.PutScanSummaryParams{ScanID: scan.ID, SummaryJson: string(raw), ListJson: string(listRaw)})
}

// Summary serves the persisted detail-free projection of a scan transition.
func (c *CoordinatorImpl) Summary(ctx context.Context, id string) (*api.CodeScan, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("scan coordinator not configured")
	}
	raw, err := c.Store.queries.GetScanSummary(ctx, id)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var summary api.CodeScan
	if err := json.Unmarshal([]byte(raw), &summary); err != nil {
		return nil, err
	}
	return &summary, nil
}
