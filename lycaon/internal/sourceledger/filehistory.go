package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// FileTrackedSince reports the first retained state time.
func (s *History) FileTrackedSince(
	ctx context.Context,
	projectID, fileID string,
) (time.Time, bool, error) {
	if s == nil {
		return time.Time{}, false, fmt.Errorf("ledger not configured")
	}
	raw, err := s.queries.GetSourceFileTrackedSince(ctx, db.GetSourceFileTrackedSinceParams{
		ProjectID: projectID, FileID: fileID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	ts, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse source file tracked timestamp: %w", err)
	}
	return ts, true, nil
}

// FileVersionGitOIDs maps content addresses to their newest version.
func (s *History) FileVersionGitOIDs(
	ctx context.Context,
	projectID, fileID string,
) (map[string]string, error) {
	if s == nil {
		return nil, fmt.Errorf("ledger not configured")
	}
	rows, err := s.queries.ListSourceFileVersionGitOIDs(ctx, db.ListSourceFileVersionGitOIDsParams{
		ProjectID: projectID, FileID: fileID,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows)*2)
	for _, row := range rows {
		// Keep the newest version for duplicate content.
		if row.GitOidSha1 != "" {
			if _, held := out[row.GitOidSha1]; !held {
				out[row.GitOidSha1] = row.VersionID
			}
		}
		if row.GitOidSha256 != "" {
			if _, held := out[row.GitOidSha256]; !held {
				out[row.GitOidSha256] = row.VersionID
			}
		}
	}
	return out, nil
}

// GitTransitionChain returns observed head movements oldest first.
func (s *History) GitTransitionChain(
	ctx context.Context,
	projectID, rootID string,
	limit int,
) ([]GitTransition, error) {
	if s == nil {
		return nil, fmt.Errorf("ledger not configured")
	}
	if limit <= 0 {
		limit = 64
	}
	rows, err := s.queries.ListSourceGitTransitionChain(ctx, db.ListSourceGitTransitionChainParams{
		ProjectID: projectID, RootID: rootID, Limit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]GitTransition, 0, len(rows))
	for _, row := range rows {
		out = append(out, gitTransitionFromRow(row))
	}
	return out, nil
}
