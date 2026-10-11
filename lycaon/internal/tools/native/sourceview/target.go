package sourceview

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReportTarget reports a resolved path as this call's file target. Granted
// paths outside the attached roots are not project files and report nothing.
func ReportTarget(tctx tools.ToolContext, resolved projectpaths.Resolved, kind api.AgentActivityKind) (agentpresence.Target, bool) {
	if resolved.External || strings.TrimSpace(resolved.Root.ID) == "" || strings.TrimSpace(resolved.ScopeRel) == "" {
		return agentpresence.Target{}, false
	}
	target := agentpresence.Target{RootID: resolved.Root.ID, Path: filepath.ToSlash(resolved.ScopeRel)}
	if tctx.Effects.Presence != nil {
		tctx.Effects.Presence.Target(target, kind)
	}
	return target, true
}

func ReadLocation(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, fullPath string) (fseffect.Location, error) {
	resolved, err := projectpaths.ResolveRead(ctx, boundary, tctx, fullPath)
	if err != nil {
		return fseffect.Location{}, err
	}
	return resolved.EffectLocation(), nil
}
