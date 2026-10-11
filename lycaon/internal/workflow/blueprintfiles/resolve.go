package blueprintfiles

import (
	"context"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

func ResolveContent(ctx context.Context, run *api.WorkflowRun, projectDir, relPath string) (string, error) {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" && run != nil {
		relPath = strings.TrimSpace(run.BlueprintPath)
	}
	if relPath == "" {
		return "", nil
	}
	return ReadBlueprintFile(projectDir, relPath)
}
