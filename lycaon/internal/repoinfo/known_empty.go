package repoinfo

import "context"

func MeasuredEmpty(ctx context.Context, p Provider, workspacePath string) bool {
	if p == nil || workspacePath == "" {
		return false
	}
	empty, err := p.KnownEmpty(ctx, workspacePath)
	return err == nil && empty
}
