package workflow

import (
	"context"
	"strings"
)

// ActivePlan returns the active run's plan id and content when present.
func (m *RunManager) ActivePlan(ctx context.Context, sessionID string) (blueprintPath, content string, ok bool) {
	if m == nil {
		return "", "", false
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return "", "", false
	}
	blueprintPath = strings.TrimSpace(active.BlueprintPath)
	if blueprintPath == "" {
		return "", "", false
	}
	if m.BlueprintGet != nil {
		if p, err := m.BlueprintGet.Get(ctx, active.ProjectID, blueprintPath); err == nil && p != nil && strings.TrimSpace(p.Content) != "" {
			return blueprintPath, p.Content, true
		}
	}
	return blueprintPath, "", true
}
