package toolexecution

import (
	"context"
	"os"
	"slices"

	"github.com/lycaon/lycaon/internal/capabilityrequest"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func (e *Boundary) preflightWriteRoot(ctx context.Context, tool string, args map[string]any, tc *tools.ToolContext) error {
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityWriteRoot) {
		return nil
	}
	request, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, reject)
	}
	if request == nil || len(request.WriteRoots) == 0 {
		return nil
	}
	roots := coalesceRequestedPaths(request.WriteRoots, true)
	for _, root := range roots {
		if _, policy := confine.AgentPolicyPath(root, tools.ConfineRootsForAction(*tc)...); policy {
			if tc.Invocation.Contract.Owner == "terminals" {
				return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{Code: "POLICY_WRITE_REQUIRES_COMMAND", Data: map[string]any{"path": root}})
			}
			if err := e.preparePolicyWrite(tc, root); err != nil {
				return err
			}
			continue
		}
		if err := e.reviewWriteRoot(ctx, tool, args, *tc, root); err != nil {
			return err
		}
	}
	return nil
}

// coalesceRequestedPaths canonicalizes aliases and drops descendants already
// covered by an ancestor directory in the same directional request.
func coalesceRequestedPaths(paths []string, write bool) []string {
	if len(paths) <= 1 {
		return paths
	}
	type item struct {
		raw   string
		canon string
		dir   bool
	}
	items := make([]item, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		canon := fspath.CanonicalPath(p)
		if canon == "" {
			canon = p
		}
		if seen[canon] {
			continue
		}
		seen[canon] = true
		isDir := write && confine.ClassifyBlockedWrite(canon).Kind == confine.WriteSubjectOrdinary
		if !isDir {
			if info, err := os.Lstat(canon); err == nil && info.IsDir() {
				isDir = true
			}
		}
		items = append(items, item{raw: p, canon: canon, dir: isDir})
	}
	slices.SortFunc(items, func(a, b item) int {
		if len(a.canon) != len(b.canon) {
			return len(a.canon) - len(b.canon)
		}
		if a.canon < b.canon {
			return -1
		}
		if a.canon > b.canon {
			return 1
		}
		return 0
	})
	var kept []item
	for _, cur := range items {
		covered := false
		for _, prev := range kept {
			if prev.dir && confine.PathAtOrUnder(cur.canon, prev.canon) &&
				confine.ClassifyBlockedWrite(cur.canon).Kind == confine.WriteSubjectOrdinary &&
				!confine.KeyMaterialPath(cur.canon) {
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, cur)
		}
	}
	out := make([]string, len(kept))
	for i, k := range kept {
		out[i] = k.raw
	}
	return out
}

// ReviewRedirectRoot reviews the destination before its producer starts.
func (e *Boundary) ReviewRedirectRoot(ctx context.Context, tc tools.ToolContext, operator, path, root string) (tools.ToolContext, error) {
	tc.Files.GrantedWriteRoots = e.actionConfineRequest(ctx, tc).GrantedWriteRoots
	if confine.PathWithinRoots(path, tc.Files.GrantedWriteRoots) {
		return tc, nil
	}
	args := map[string]any{"command": operator + " " + path}
	if err := e.reviewWriteRoot(ctx, "command", args, tc, root); err != nil {
		return tc, err
	}
	tc.Files.GrantedWriteRoots = e.actionConfineRequest(ctx, tc).GrantedWriteRoots
	return tc, nil
}

func (e *Boundary) reviewWriteRoot(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, root string) error {
	if e.writeRootPreflight == nil {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": "write-root approval is not configured"},
		})
	}
	authorized, denied, guidance, err := e.writeRootPreflight(ctx, tool, args, tc, root)
	if err != nil {
		return e.Approvals.rejectApprovalErr(ctx, tool, tc.Identity.Agent, args, err)
	}
	if !authorized {
		data := map[string]any{"path": root}
		if guidance != "" {
			data[toolrejection.UserGuidanceKey] = guidance
		}
		code := isolation.CodeWriteRootDenied
		if !denied {
			code = isolation.CodeApprovalUnavailable
			data["reason"] = "approval did not install authority"
		}
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
			Code: code, Data: data,
		})
	}
	return nil
}
