package call

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/tools"
)

func registerHandoffReservationTools(reg *tools.DefaultRegistry, deps HandoffToolDeps) error {
	if err := reg.Register("handoff_reserve", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		sessionID := handoffSessionID(args, tctx)
		agent := handoffAgentID(args, tctx)
		paths, err := parsePathList(args["paths"])
		if err != nil {
			return "", err
		}
		out, err := deps.Calls.Reserve(ctx, sessionID, paths, agent)
		if err != nil {
			return "", err
		}
		if tctx.Effects.Presence != nil && out != nil {
			tctx.Effects.Presence.Reserved(reservationTargets(tctx, out.Reserved))
		}
		raw, _ := json.Marshal(out)
		return string(raw), nil
	}); err != nil {
		return err
	}

	if err := reg.Register("handoff_release", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		sessionID := handoffSessionID(args, tctx)
		agent := handoffAgentID(args, tctx)
		paths, err := parsePathList(args["paths"])
		if err != nil {
			return "", err
		}
		if err := deps.Calls.Release(ctx, sessionID, paths, agent); err != nil {
			return "", err
		}
		if targets := reservationTargets(tctx, paths); tctx.Effects.Presence != nil && len(targets) > 0 {
			tctx.Effects.Presence.Released(targets)
		}
		return `{"released":true}`, nil
	}); err != nil {
		return err
	}

	return reg.Register("handoff_release_all", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		sessionID := handoffSessionID(args, tctx)
		agent := handoffAgentID(args, tctx)
		if err := deps.Calls.ReleaseAll(ctx, sessionID, agent); err != nil {
			return "", err
		}
		if tctx.Effects.Presence != nil {
			tctx.Effects.Presence.Released(nil)
		}
		return `{"released_all":true}`, nil
	})
}

// reservationTargets places reserved paths in the session's active root. Only
// normalized project-relative paths name files.
func reservationTargets(tctx tools.ToolContext, paths []string) []agentpresence.Target {
	rootID := strings.TrimSpace(tctx.Source.ActiveRootID)
	if rootID == "" {
		return nil
	}
	out := make([]agentpresence.Target, 0, len(paths))
	for _, raw := range paths {
		if path, err := NormalizeRelativePath(raw); err == nil {
			out = append(out, agentpresence.Target{RootID: rootID, Path: path})
		}
	}
	return out
}
