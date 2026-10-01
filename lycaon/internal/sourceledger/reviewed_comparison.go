package sourceledger

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lycaon/lycaon/internal/db"
)

// CompareReviewed reads the immutable range covered by a particular look.
func (s *Store) CompareReviewed(ctx context.Context, projectID, fileID string, through int64) (Comparison, error) {
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	watermark, err := s.queries.GetSourcePresentationWatermark(ctx, db.GetSourcePresentationWatermarkParams{
		ProjectID: projectID, FileID: fileID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Comparison{}, ErrPresentationNotFound
	}
	if err != nil {
		return Comparison{}, err
	}
	if through <= 0 || watermark.ThroughOrdinal != through || watermark.SeenAfterOrdinal >= through {
		return Comparison{}, ErrPresentationMismatch
	}
	first, err := s.queries.OldestSourceEffectForFileAfterOrdinal(ctx, db.OldestSourceEffectForFileAfterOrdinalParams{
		ProjectID: projectID, FileID: fileID, Ordinal: watermark.SeenAfterOrdinal,
	})
	if err != nil {
		return Comparison{}, err
	}
	if first.Ordinal > through {
		return Comparison{}, ErrPresentationMismatch
	}
	last, err := s.queries.GetSourceEffect(ctx, watermark.DisplayedEffectID)
	if err != nil {
		return Comparison{}, err
	}
	out, err := s.compareVersions(ctx, projectID, first.BeforeVersionID, last.AfterVersionID)
	if err != nil {
		return Comparison{}, err
	}
	out.InRange, out.FileID = true, fileID
	out.LocationChanged = out.Before.RootID != out.After.RootID || out.Before.Path != out.After.Path
	if err := s.attachComparisonAttribution(ctx, Baseline{}, ScopeComparisonOptions{}, &out); err != nil {
		return Comparison{}, err
	}
	return out, nil
}
