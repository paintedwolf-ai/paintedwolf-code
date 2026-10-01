package scan

import (
	"context"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// metadata reads lifecycle and authority facts without raw results or findings.
func (s *SQLStore) metadata(ctx context.Context, ids []string) ([]api.CodeScan, error) {
	rows, err := s.queries.ScanMetadata(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]api.CodeScan, 0, len(rows))
	for _, row := range rows {
		scan, err := scanMetadata(row)
		if err != nil {
			return nil, err
		}
		out = append(out, scan)
	}
	return out, nil
}

func scanMetadata(row db.ScanMetadataRow) (api.CodeScan, error) {
	s := api.CodeScan{
		ID: row.ID, CanonicalPath: row.CanonicalPath, ScannerID: db.StringFromNull(row.ScannerID),
		Status: api.CodeScanStatus(row.Status), HeadSHA: row.HeadSha, SourceSnapshotID: row.SourceSnapshotID,
		Trigger: api.ScanTrigger(row.Trigger), Error: db.StringFromNull(row.Error),
		AssessmentID: row.AssessmentID, CoverageStatus: api.ScanCoverageStatus(row.CoverageStatus),
		FindingSetID: row.FindingSetID, ExecutionFingerprint: row.ExecutionFingerprint,
		FingerprintScheme: row.FingerprintScheme, FailureCode: row.FailureCode,
	}
	s.CreatedAt, _ = db.ParseTime(row.CreatedAt)
	if row.StartedAt.Valid {
		at, err := db.ParseTime(row.StartedAt.String)
		if err != nil {
			return s, err
		}
		s.StartedAt = &at
	}
	if row.CompletedAt.Valid {
		at, err := db.ParseTime(row.CompletedAt.String)
		if err != nil {
			return s, err
		}
		s.CompletedAt = &at
	}
	if row.LongRunningAt.Valid {
		at, err := db.ParseTime(row.LongRunningAt.String)
		if err != nil {
			return s, err
		}
		s.LongRunningAt = &at
		s.LongRunning = true
	}
	for _, field := range []struct {
		raw    string
		target any
	}{
		{row.CategoriesJson, &s.Categories}, {row.ExecutionManifestJson, &s.ExecutionManifest},
		{row.RuntimeJson, &s.Runtime},
	} {
		if field.raw != "" {
			if err := json.Unmarshal([]byte(field.raw), field.target); err != nil {
				return s, err
			}
		}
	}
	return s, nil
}
