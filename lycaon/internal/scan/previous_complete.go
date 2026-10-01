package scan

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// PreviousComplete returns the prior authoritative run whose scanner execution
// and finding identity are comparable with latest.
func (s *SQLStore) PreviousComplete(ctx context.Context, latest api.CodeScan) (*api.CodeScan, error) {
	if s == nil {
		return nil, nil
	}
	if latest.ID == "" {
		return nil, nil
	}
	rows, err := s.queries.ListScansByCanonicalPath(ctx, latest.CanonicalPath)
	if err != nil {
		return nil, err
	}
	scans := codeScansFromRows(rows)
	for index := range scans {
		if err := hydrateScanFacts(ctx, s.db, &scans[index]); err != nil {
			return nil, err
		}
	}
	seenLatest := false
	for index := range scans {
		candidate := &scans[index]
		if !seenLatest {
			seenLatest = candidate.ID == latest.ID
			continue
		}
		if candidate.Status != api.CodeScanStatusComplete || !SameCategories(candidate.Categories, latest.Categories) {
			continue
		}
		if delegationID := strings.TrimSpace(latest.DelegationID); delegationID != "" {
			if candidate.DelegationID != delegationID {
				continue
			}
		} else if candidate.CanonicalPath != latest.CanonicalPath {
			continue
		}
		if candidate.ScannerID != latest.ScannerID ||
			candidate.ExecutionFingerprint != latest.ExecutionFingerprint ||
			candidate.FingerprintScheme != latest.FingerprintScheme ||
			!CoversGeneration(candidate.CoverageStatus) || candidate.FindingSetID == "" {
			continue
		}
		return s.hydrateScan(ctx, candidate)
	}
	return nil, nil
}
