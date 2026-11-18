package drafts

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/timelayout"
)

// Publication consumes only the draft whose bytes reached the project.
func (s *SQL) DeletePublished(ctx context.Context, draft Record) error {
	return s.queries.DeletePublishedWorkflowDraft(ctx, db.DeletePublishedWorkflowDraftParams{
		SessionID: draft.SessionID, WorkflowID: draft.WorkflowID, Version: draft.Version, ManifestYaml: draft.ManifestYAML, AuthoredYaml: draft.AuthoredYAML, CreatedAt: timelayout.Format(draft.CreatedAt),
	})
}

func (s *Memory) DeletePublished(ctx context.Context, draft Record) error {
	current, err := s.Get(ctx, draft.SessionID, draft.WorkflowID, draft.Version)
	if err == ErrNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if current.CreatedAt != draft.CreatedAt || current.ManifestYAML != draft.ManifestYAML || current.AuthoredYAML != draft.AuthoredYAML {
		return nil
	}
	return s.Delete(ctx, draft.SessionID, draft.WorkflowID, draft.Version)
}
