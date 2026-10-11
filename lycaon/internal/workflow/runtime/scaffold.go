package runtime

import (
	"context"
)

// ScaffoldVarsForSession returns scaffold vars for the active workflow run, if any.
func (m *SessionPolicy) ScaffoldVarsForSession(ctx context.Context, sessionID string) (map[string]any, error) {
	if m == nil {
		return nil, nil
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return nil, err
	}
	return m.Runs.GetScaffoldVars(ctx, active.ID)
}
