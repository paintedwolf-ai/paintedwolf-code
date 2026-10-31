package native

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/hostos"
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
		return "", &toolrejection.ToolReject{
			Code: "CHOWN_RECURSIVE_DENIED",
			Data: map[string]any{"recursive": true},
		}
	}
	if !chownSupported() {
		return "", chownUnsupported()
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
	uid, gid, err := resolveChownIdentities(hostos.Host{}, ownerSpec, groupSpec)
	if err != nil {
		return "", err
	}
	results, err := t.Chown(ctx, ChownInput{Paths: paths, UID: uid, GID: gid}, tctx)
	resp := chownResponse{Results: results}
	if resp.Results == nil {
		resp.Results = []chownResult{}
	}
	if err != nil {
		if len(resp.Results) > 0 {
			return interruptedMutationBatch(resp, err)
		}
		return "", err
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ChownInput is one ownership change. UID and GID are resolved identities;
// a negative GID keeps each path's current group.
type ChownInput struct {
	Paths    []string
	UID, GID int
}

// ResolveChownIdentities applies the existing identity policy to numeric or
// named owner and group specifications.
func ResolveChownIdentities(owner, group string) (int, int, error) {
	return resolveChownIdentities(hostos.Host{}, owner, group)
}

// Chown changes each path's ownership under scope, review and the source
// journal, returning the paths already changed when one fails.
func (t *ChownTool) Chown(ctx context.Context, in ChownInput, tctx tools.ToolContext) ([]chownResult, error) {
	var results []chownResult
	prof := tctx.ProfileID()
	for _, relPath := range in.Paths {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		target, uidBefore, gidBefore, err := assertChownTarget(ctx, t.Boundary, tctx, relPath, prof)
		if err != nil {
			return results, err
		}
		gid := in.GID
		if gid < 0 {
			gid = gidBefore
		}
		if err := chownAgentPath(ctx, tctx, target, in.UID, gid); err != nil {
			return results, fmt.Errorf("chown %s: %w", relPath, err)
		}
		results = append(results, chownResult{Path: filepath.ToSlash(relPath), UIDBefore: uidBefore, GIDBefore: gidBefore, UIDAfter: in.UID, GIDAfter: gid})
	}
	return results, nil
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
