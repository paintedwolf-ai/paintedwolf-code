package projectsource

import (
	"context"
	"time"
)

// Retain the unavailable entry while letting other history actions proceed.
func (s *SourceHistory) markUnavailable(ctx context.Context, projectID, id, direction string) error {
	state := "applied"
	if direction == "redo" {
		state = "undone"
	}
	if s.db == nil {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		if entry := s.history[id]; entry != nil && entry.ProjectID == projectID && entry.State == state {
			entry.Unavailable = true
		}
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE source_history_entries SET availability='unavailable',updated_at=? WHERE project_id=? AND id=? AND state=? AND availability='available'`, time.Now().UTC().Format(time.RFC3339Nano), projectID, id, state)
	return err
}
