package workflow

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ResolveBlueprintContent loads blueprint bytes for human_approval and gate evaluation.
// Content is always the convention file at relPath (or run.BlueprintPath when relPath empty).
func ResolveBlueprintContent(ctx context.Context, m *RunManager, run *api.WorkflowRun, projectDir, relPath string) (string, error) {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" && run != nil {
		relPath = strings.TrimSpace(run.BlueprintPath)
	}
	if relPath == "" {
		return "", nil
	}
	return ReadBlueprintFile(projectDir, relPath)
}

// BlueprintGetter loads blueprint files for gate evaluation.
type BlueprintGetter interface {
	Get(ctx context.Context, projectID, path string) (*api.Blueprint, error)
}
