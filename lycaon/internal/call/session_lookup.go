package call

import (
	"context"
	"strings"
)

// StoreSessionLookup adapts a minimal session getter for project binding.
type StoreSessionLookup struct {
	Get func(ctx context.Context, id string) (projectDir string, err error)
}

// ProjectDir returns the project_dir for a session id.
func (l StoreSessionLookup) ProjectDir(ctx context.Context, sessionID string) (string, error) {
	if l.Get == nil {
		return "", ErrSessionNotFound
	}
	dir, err := l.Get(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return "", err
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", ErrSessionNotFound
	}
	return dir, nil
}
