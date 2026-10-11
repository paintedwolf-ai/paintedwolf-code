package blueprints

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/blueprint"
	"strings"
)

// PolicyContent reads policy blueprint facts and distinguishes a missing blueprint from an unavailable store.
func (m *Service) PolicyContent(ctx context.Context, projectID, path string) (string, error) {
	if path == "" || m.Getter == nil {
		return "", nil
	}
	plan, err := m.Getter.Get(ctx, projectID, path)
	if errors.Is(err, blueprint.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read workflow policy blueprint: %w", err)
	}
	if plan == nil || strings.TrimSpace(plan.Content) == "" {
		return "", nil
	}
	return plan.Content, nil
}
