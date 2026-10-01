package store

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/lycaon/lycaon/internal/db"
)

const maintenanceSessionBatch = 256

// WorkspaceNotificationIDs returns one page without hydrating history.
func (s *SQL) WorkspaceNotificationIDs(ctx context.Context, root, after string) ([]string, error) {
	return s.queries.ListWorkspaceNotificationSessionIDs(ctx, db.ListWorkspaceNotificationSessionIDsParams{
		RootPath: root, AfterID: after, PageLimit: maintenanceSessionBatch,
	})
}

// ExistingSessionIDs checks one bounded set, including archived owners.
func (s *SQL) ExistingSessionIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	result := make(map[string]bool)
	for start := 0; start < len(ids); start += maintenanceSessionBatch {
		page, err := json.Marshal(ids[start:min(start+maintenanceSessionBatch, len(ids))])
		if err != nil {
			return nil, err
		}
		rows, err := s.queries.ListExistingSessionIDs(ctx, string(page))
		if err != nil {
			return nil, err
		}
		for _, id := range rows {
			result[id] = true
		}
	}
	return result, nil
}

func (s *Memory) WorkspaceNotificationIDs(ctx context.Context, root, after string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ids []string
	for id, sess := range s.sessions {
		if sess.WorkspacePath == root && sess.ArchivedAt == nil && id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids[:min(len(ids), maintenanceSessionBatch)], nil
}

func (s *Memory) ExistingSessionIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]bool)
	for _, id := range ids {
		if _, ok := s.sessions[id]; ok {
			result[id] = true
		}
	}
	return result, nil
}
