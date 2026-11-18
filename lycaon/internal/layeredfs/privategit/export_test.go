package privategit

import "context"

// viewWorktreeDir returns the private worktree stage directory for viewID.
func viewWorktreeDir(m *Manager, viewID string) string {
	v, err := getManagedView(m, viewID)
	if err != nil {
		return ""
	}
	return v.worktreeDir
}

// SetAccessOrigin sets the active confinement origin for a view (filesystem vs native_tool).
func SetAccessOrigin(m *Manager, viewID string, origin AccessOrigin) error {
	view, err := getManagedView(m, viewID)
	if err != nil {
		return err
	}
	view.mu.Lock()
	view.accessOrigin = origin
	view.mu.Unlock()
	return nil
}

func withGCTestHooks(ctx context.Context, hooks gcTestHooks) context.Context {
	return context.WithValue(ctx, gcTestHooksKey{}, hooks)
}
