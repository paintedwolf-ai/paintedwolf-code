package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprint"
)

func readPolicyBlueprint(ctx context.Context, getter BlueprintGetter, projectID, path string) (string, error) {
	if path == "" || getter == nil {
		return "", nil
	}
	plan, err := getter.Get(ctx, projectID, path)
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
