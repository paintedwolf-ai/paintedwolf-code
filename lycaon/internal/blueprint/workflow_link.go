package blueprint

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkflowBlueprintCreator adapts Manager for workflow RunManager.Blueprints.Creator.
type WorkflowBlueprintCreator struct {
	Manager *Manager
}

// CreateBlueprint writes a draft blueprint file for a workflow run; returns its path.
func (w WorkflowBlueprintCreator) CreateBlueprint(ctx context.Context, projectID, title, path, sourceWorkflowID string) (string, error) {
	if w.Manager == nil {
		return "", nil
	}
	p, err := w.Manager.Create(ctx, projectID, title, path, sourceWorkflowID, "")
	if err != nil {
		return "", err
	}
	return p.Path, nil
}

// RetargetToTitle moves a provisional draft onto a declared title.
func (w WorkflowBlueprintCreator) RetargetToTitle(ctx context.Context, projectID, path, title string) (string, error) {
	if w.Manager == nil {
		return path, nil
	}
	return w.Manager.RetargetToTitle(ctx, projectID, path, title)
}

// ResolveDraft ensures a blueprint exists at path and is still draft.
func (w WorkflowBlueprintCreator) ResolveDraft(ctx context.Context, projectID, blueprintPath string) error {
	if w.Manager == nil {
		return nil
	}
	p, err := w.Manager.Get(ctx, projectID, blueprintPath)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if p.Status != api.BlueprintStatusDraft {
		return ErrInvalidStatus
	}
	return nil
}
