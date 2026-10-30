package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// AgentPolicyCheck is frozen from the request that proposed the operation.
type AgentPolicyCheck func(context.Context, []agentpolicy.Target) error

type agentPolicyKey struct{}

func WithAgentPolicy(ctx context.Context, check AgentPolicyCheck) context.Context {
	return context.WithValue(ctx, agentPolicyKey{}, check)
}

// CheckPathPolicy checks the most specific attached root of a resolved path.
func (tc ToolContext) CheckPathPolicy(ctx context.Context, path string, mutation bool) error {
	return tc.CheckPolicyTargets(ctx, []agentpolicy.Target{{Path: path, Mutation: mutation}})
}

// CheckPolicyTargets reviews all endpoints in one pre-effect policy snapshot.
func (tc ToolContext) CheckPolicyTargets(ctx context.Context, targets []agentpolicy.Target) error {
	check, _ := ctx.Value(agentPolicyKey{}).(AgentPolicyCheck)
	if check == nil {
		return nil
	}
	var bound []agentpolicy.Target
	seen := map[agentpolicy.Target]bool{}
	for _, target := range targets {
		if target.Path == "" {
			continue
		}
		if !filepath.IsAbs(target.Path) {
			return fmt.Errorf("policy admission requires a resolved absolute path")
		}
		var rootID, rootPath string
		for _, root := range tc.Source.Roots {
			rel, err := filepath.Rel(root.Path, target.Path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			if len(root.Path) > len(rootPath) {
				rootID, rootPath = root.ID, root.Path
			}
		}
		if rootID == "" {
			continue
		}
		target.RootID = rootID
		if !seen[target] {
			seen[target] = true
			bound = append(bound, target)
		}
	}
	if len(bound) == 0 {
		return nil
	}
	return check(ctx, bound)
}

// CheckScopePolicy resolves a reached operation's catalog scope before admission.
func (tc ToolContext) CheckScopePolicy(ctx context.Context, rootID, path string, mutation bool) error {
	if check, _ := ctx.Value(agentPolicyKey{}).(AgentPolicyCheck); check == nil {
		return nil
	}
	if path == "" {
		path = "."
	}
	abs, _, err := projectroot.ResolveAbs(tc.Source.Roots, rootID, path)
	if err != nil {
		return err
	}
	return tc.CheckPathPolicy(ctx, abs, mutation)
}
