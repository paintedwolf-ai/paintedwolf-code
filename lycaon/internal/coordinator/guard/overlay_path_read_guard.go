package guard

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// CoordinatorOverlayPathReadForbiddenCode rejects coordinator access under worker sandboxes.
const CoordinatorOverlayPathReadForbiddenCode = "COORDINATOR_OVERLAY_PATH_READ_FORBIDDEN"

// ObserveCoordinatorWorkerBranchPath stamps path_is_worker_branch when a
// coordinator tool names a worker-branch path.
func ObserveCoordinatorWorkerBranchPath(
	sess *api.Session,
	toolName string,
	args map[string]any,
	gc *oar.GuardContext,
) {
	if gc == nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	gc.Invocation.Tool = toolName
	gc.DeriveToolClassFacts()
	path := coordinatorWorkerBranchPath(args)
	gc.Workers.PathIsWorkerBranch = path != ""
	if gc.Workers.PathIsWorkerBranch {
		gc.PutRejectData(CoordinatorOverlayPathReadForbiddenCode, map[string]any{
			"tool": toolName,
			"path": enginepaths.WorkerBranchDisplayRel(path),
		})
	}
}

func coordinatorWorkerBranchPath(args map[string]any) string {
	if args == nil {
		return ""
	}
	for _, key := range []string{"path", "cwd"} {
		if raw, ok := args[key].(string); ok {
			if p := normalizeCoordinatorPath(raw); enginepaths.IsWorkerBranchPath(p) {
				return p
			}
		}
	}
	switch raw := args["paths"].(type) {
	case []string:
		for _, item := range raw {
			if p := normalizeCoordinatorPath(item); enginepaths.IsWorkerBranchPath(p) {
				return p
			}
		}
	case []any:
		for _, item := range raw {
			s, ok := item.(string)
			if !ok {
				continue
			}
			if p := normalizeCoordinatorPath(s); enginepaths.IsWorkerBranchPath(p) {
				return p
			}
		}
	}
	return ""
}

func normalizeCoordinatorPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return filepath.ToSlash(raw)
}
