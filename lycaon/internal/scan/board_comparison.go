package scan

import (
	"context"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// refreshBoardComparisons publishes comparisons alongside the assessment change.
func refreshBoardComparisons(ctx context.Context, q *db.Queries, scanID string) error {
	rows, err := q.ScanMetadata(ctx, []string{scanID})
	if err != nil || len(rows) == 0 {
		return err
	}
	assessments, err := q.BoardAssessmentCandidates(ctx, rows[0].CanonicalPath)
	if err != nil {
		return err
	}
	var complete [][]api.CodeScan
	for _, assessment := range assessments {
		bindings, err := q.ListAssessmentScanBindings(ctx, []string{assessment.ID})
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(bindings))
		for _, b := range bindings {
			ids = append(ids, b.ScanID)
		}
		rows, err := q.ScanMetadata(ctx, ids)
		if err != nil {
			return err
		}
		members := make([]api.CodeScan, 0, len(rows))
		for _, row := range rows {
			member, err := scanMetadata(row)
			if err != nil {
				return err
			}
			members = append(members, member)
		}
		var required []string
		if err := json.Unmarshal([]byte(assessment.RequiredScannersJson), &required); err != nil {
			return err
		}
		if assessmentComplete(required, members) {
			complete = append(complete, members)
		}
	}
	if len(complete) < 2 {
		return nil
	}
	for _, current := range complete[0] {
		for _, previous := range complete[1] {
			if previous.ScannerID != current.ScannerID || validateComparison(&previous, &current) != nil {
				continue
			}
			if _, err := q.GetBoardComparison(ctx, db.GetBoardComparisonParams{OldSetID: previous.FindingSetID, NewSetID: current.FindingSetID}); err == nil {
				continue
			} else if !db.IsNoRows(err) {
				return err
			}
			if _, err := comparisonProjection(ctx, q, &previous, &current); err != nil {
				return err
			}
		}
	}
	return nil
}

// BoardComparison only reads a committed projection; opening never computes a diff.
func (c *CoordinatorImpl) BoardComparison(ctx context.Context, oldScan, newScan api.CodeScan) (*Comparison, error) {
	if err := validateComparison(&oldScan, &newScan); err != nil {
		return nil, err
	}
	raw, err := c.Store.queries.GetBoardComparison(ctx, db.GetBoardComparisonParams{
		OldSetID: oldScan.FindingSetID, NewSetID: newScan.FindingSetID,
	})
	if err != nil {
		return nil, err
	}
	var response Comparison
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return nil, err
	}
	return &response, nil
}
