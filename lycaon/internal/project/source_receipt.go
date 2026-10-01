package project

import (
	"context"
	"encoding/json"
)

// CommittedSourceResult reads the authoritative filesystem journal after recovery.
func (s *SourceMutationService) CommittedSourceResult(ctx context.Context, id string) (json.RawMessage, bool, error) {
	row, found, err := s.load(ctx, id)
	if err != nil || !found {
		return nil, false, err
	}
	return row.Response, row.Status == sourceMutationCommitted, nil
}
