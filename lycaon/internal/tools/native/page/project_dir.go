package page

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// resolveCaptureProjectDir resolves paths in the caller's workspace view.
func resolveCaptureProjectDir(ctx context.Context, tctx tools.ToolContext, modelPath string) (string, error) {
	modelPath = strings.TrimSpace(modelPath)
	if modelPath == "" {
		return "", nil
	}
	if len(tctx.Roots) == 0 {
		return "", &toolrejection.ToolReject{Code: "CAPTURE_PROJECT_DIR_MISSING", Data: map[string]any{"path": modelPath}}
	}
	resolved, err := projectpaths.ResolveRead(ctx, nil, tctx, modelPath)
	if err != nil {
		return "", &toolrejection.ToolReject{Code: "CAPTURE_NAVIGATION_DENIED", Data: map[string]any{
			"reason":                 "project_dir_escape",
			"capture_project_escape": true,
			"path":                   modelPath,
		}}
	}
	return resolved.Abs, nil
}
