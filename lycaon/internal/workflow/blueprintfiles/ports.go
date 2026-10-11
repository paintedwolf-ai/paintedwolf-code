package blueprintfiles

import (
	"context"
	"github.com/lycaon/lycaon/pkg/api"
)

type Creator interface {
	CreateBlueprint(ctx context.Context, projectID, title, path, sourceWorkflowID string) (blueprintPath string, err error)
	ResolveDraft(ctx context.Context, projectID, blueprintPath string) error
	RetargetToTitle(ctx context.Context, projectID, path, title string) (string, error)
}
type Getter interface {
	Get(ctx context.Context, projectID, path string) (*api.Blueprint, error)
}
