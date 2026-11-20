package store

import (
	"context"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/db"
)

const maintenanceSessionBatch = 256

// WorkspaceNotificationIDs returns one page without hydrating history.
func (s *sqlSessionViews) WorkspaceNotificationIDs(ctx context.Context, root, after string) ([]string, error) {
	return s.core.queries.ListWorkspaceNotificationSessionIDs(ctx, db.ListWorkspaceNotificationSessionIDsParams{
		RootPath: root, AfterID: after, PageLimit: maintenanceSessionBatch,
	})
}

// ExistingSessionIDs checks one bounded set, including archived owners.
func (s *sqlSessionViews) ExistingSessionIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	result := make(map[string]bool)
	for start := 0; start < len(ids); start += maintenanceSessionBatch {
		page, err := json.Marshal(ids[start:min(start+maintenanceSessionBatch, len(ids))])
		if err != nil {
			return nil, err
		}
		rows, err := s.core.queries.ListExistingSessionIDs(ctx, string(page))
		if err != nil {
			return nil, err
		}
		for _, id := range rows {
			result[id] = true
		}
	}
	return result, nil
}
