package runstate

import "context"

type ScaffoldRepository interface {
	GetVars(ctx context.Context, sessionID string) (map[string]any, error)
	UpsertVars(ctx context.Context, sessionID string, vars map[string]any) error
}
