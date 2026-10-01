package scan

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// FindingsNeedingConfirmation returns absent findings a covering scan re-checks.
// Absence recorded without coverage or under another execution identity stays
// eligible for confirmation without counting as an open finding.
func (s *SQLStore) FindingsNeedingConfirmation(ctx context.Context, job *api.CodeScan) (map[string]api.SecurityFinding, error) {
	if job.ExecutionFingerprint == "" || !(job.CoverageStatus == api.ScanCoverageComplete ||
		(job.TargetKind == api.ScanTargetPaths && job.CoverageStatus == api.ScanCoverageBounded)) {
		return nil, nil
	}
	filter, err := newLedgerFilter(job.CanonicalPath, api.FindingLedgerQueryRequest{
		ScannerIDs: []string{job.ScannerID},
		States:     []api.FindingLedgerState{api.FindingLedgerFixed, api.FindingLedgerNotObserved, api.FindingLedgerUnverified},
	})
	if err != nil {
		return nil, fmt.Errorf("filter finding confirmations: %w", err)
	}
	findings := make(map[string]api.SecurityFinding)
	for offset := int64(0); ; offset += MaxLedgerPageSize {
		rows, err := s.queries.ListFindingLedger(ctx, db.ListFindingLedgerParams{
			CanonicalPath: filter.CanonicalPath, FilterJson: filter.FilterJson,
			PageLimit: MaxLedgerPageSize, PageOffset: offset,
		})
		if err != nil {
			return nil, fmt.Errorf("read finding confirmations: %w", err)
		}
		for _, row := range rows {
			if row.LastScanID == job.ID || (row.State == string(api.FindingLedgerFixed) && row.LeftExecution == job.ExecutionFingerprint) {
				continue
			}
			var finding api.SecurityFinding
			if err := json.Unmarshal([]byte(row.FindingJson), &finding); err != nil {
				return nil, fmt.Errorf("decode finding confirmation %s: %w", row.Fingerprint, err)
			}
			findings[row.Fingerprint] = finding
		}
		if len(rows) < MaxLedgerPageSize {
			return findings, nil
		}
	}
}
