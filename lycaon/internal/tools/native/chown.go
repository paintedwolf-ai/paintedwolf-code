package native

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// ChownTool changes file ownership within write scope (POSIX uid/gid only).
type ChownTool struct {
	Boundary *sandbox.Boundary
}

type chownResult struct {
	Path      string `json:"path"`
	UIDBefore int    `json:"uid_before"`
	GIDBefore int    `json:"gid_before"`
	UIDAfter  int    `json:"uid_after"`
	GIDAfter  int    `json:"gid_after"`
}

type chownResponse struct {
	Results []chownResult `json:"results"`
}

func (t *ChownTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (output string, runErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if recursiveArg(args) {
		return "", &tools.ToolReject{
			Code: "CHOWN_RECURSIVE_DENIED",
			Data: map[string]any{"recursive": true},
		}
	}
	if !chownSupported() {
		return "", &tools.ToolReject{Code: "CHOWN_UNSUPPORTED", Data: nil}
	}
	paths, err := parseChownPaths(args)
	if err != nil {
		return "", err
	}
	ownerSpec, _ := args["owner"].(string)
	if strings.TrimSpace(ownerSpec) == "" {
		return "", toolkit.MissingArg("owner")
	}
	groupSpec, _ := args["group"].(string)
	uid, gid, err := resolveChownIdentities(ownerSpec, groupSpec)
	if err != nil {
		return "", err
	}
	prof := tctx.ProfileID()
	resp := chownResponse{Results: make([]chownResult, 0, len(paths))}
	defer func() {
		if runErr != nil && len(resp.Results) > 0 {
			output, runErr = interruptedMutationBatch(resp, runErr)
		}
	}()
	for _, relPath := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		target, uidBefore, gidBefore, err := assertChownTarget(ctx, t.Boundary, tctx, relPath, prof)
		if err != nil {
			return "", err
		}
		if err := chownAgentPath(ctx, tctx, target, uid, gid); err != nil {
			return "", fmt.Errorf("chown %s: %w", relPath, err)
		}
		resp.Results = append(resp.Results, chownResult{
			Path:      filepath.ToSlash(relPath),
			UIDBefore: uidBefore,
			GIDBefore: gidBefore,
			UIDAfter:  uid,
			GIDAfter:  gid,
		})
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func recursiveArg(args map[string]any) bool {
	switch v := args["recursive"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}
