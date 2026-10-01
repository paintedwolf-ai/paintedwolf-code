package toolapi

import (
	"context"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// resolveScanRoot returns the active project root, or the shared structured
// reject every path tool returns when no folder is attached.
func resolveScanRoot(ctx context.Context, tctx tools.ToolContext) (string, error) {
	resolved, err := projectpaths.ResolveRead(ctx, nil, tctx, ".")
	if err != nil {
		return "", err
	}
	return resolved.Root.Path, nil
}

// resolveScanPaths turns the scan_pack `paths` argument into scope-relative
// paths. EffectiveScanTargets joins paths verbatim, so resolving first keeps
// `..` inside the attached roots and gives `@label/path` its usual meaning.
func resolveScanPaths(ctx context.Context, raw any, tctx tools.ToolContext) ([]string, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		modelPath, isString := item.(string)
		if !isString {
			continue
		}
		resolved, err := projectpaths.ResolveRead(ctx, nil, tctx, modelPath)
		if err != nil {
			return nil, err
		}
		if resolved.ScopeRel == "" {
			continue
		}
		out = append(out, resolved.ScopeRel)
	}
	return out, nil
}
