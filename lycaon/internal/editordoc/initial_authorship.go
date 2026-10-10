package editordoc

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// Seed shared text identities with the ledger's recorded authorship.
func (s *Service) initialAuthorship(ctx context.Context, d *Document, client uint32) ([]sourceledger.TextContribution, error) {
	prior, err := s.history.QueryAttribution(ctx, d.ProjectID, d.BranchID, d.RootID, d.Path)
	if err != nil || prior.HeadSHA256 != d.BaseSHA256 || d.Draft != d.BaseContent {
		return nil, err
	}
	text := utf16.Encode([]rune(d.Draft))
	starts := []int{0}
	for i, ch := range text {
		if ch == '\n' {
			starts = append(starts, i+1)
		}
	}
	result := make([]sourceledger.TextContribution, 0, len(prior.Intervals))
	for _, interval := range prior.Intervals {
		if interval.StartLine < 1 || interval.StartLine > len(starts) {
			continue
		}
		start, end := starts[interval.StartLine-1], len(text)
		if interval.EndLine < len(starts) {
			end = starts[interval.EndLine]
		}
		if start < 0 || end < 0 || uint64(start) > math.MaxUint32 || uint64(end) > math.MaxUint32 {
			return nil, errors.New("source attribution exceeds text identity capacity")
		}
		if end <= start {
			continue
		}
		result = append(result, sourceledger.TextContribution{ProjectID: d.ProjectID, FileID: d.FileID, DocumentID: d.ID,
			Epoch: 1, Revision: d.Revision, OperationID: uuid.NewString(), Origin: interval.Origin, PersonID: interval.PersonID,
			SessionID: interval.SessionID, Turn: interval.Turn, ToolCallID: interval.ToolCallID,
			Inserted: []sourceledger.TextIdentityRange{{Client: client, Start: uint32(start), End: uint32(end)}}, CreatedAt: interval.TS})
	}
	return result, nil
}

func recordInitialAuthorship(ctx context.Context, tx *sql.Tx, contributions []sourceledger.TextContribution) error {
	for _, contribution := range contributions {
		if err := sourceledger.RecordTextContributionTx(ctx, tx, contribution); err != nil {
			return err
		}
	}
	return nil
}
