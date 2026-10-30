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
		if target.RootID != "" {
			root, ok := projectroot.RootRefByID(tc.Source.Roots, target.RootID)
			if !ok {
				return fmt.Errorf("policy root %q: %w", target.RootID, projectroot.ErrUnknownRootLabel)
			}
			if !policyPathWithinRoot(root.Path, target.Path) {
				return fmt.Errorf("policy root %q: %w", target.RootID, projectroot.ErrPathEscape)
			}
		} else {
			var rootPath string
			for _, root := range tc.Source.Roots {
				if policyPathWithinRoot(root.Path, target.Path) && len(root.Path) > len(rootPath) {
					target.RootID, rootPath = root.ID, root.Path
				}
			}
		}
		if target.RootID == "" {
			continue
		}
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

func policyPathWithinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CheckScopePolicy preserves resolved root identity and resolves declared relative scopes.
func (tc ToolContext) CheckScopePolicy(ctx context.Context, rootID, path string, mutation bool) error {
	if check, _ := ctx.Value(agentPolicyKey{}).(AgentPolicyCheck); check == nil {
		return nil
	}
	if path == "" {
		path = "."
	}
	if filepath.IsAbs(path) {
		if rootID != "" {
			if _, ok := projectroot.RootRefByID(tc.Source.Roots, rootID); !ok {
				return fmt.Errorf("policy root %q: %w", rootID, projectroot.ErrUnknownRootLabel)
			}
		}
		attached := false
		for _, root := range tc.Source.Roots {
			attached = attached || policyPathWithinRoot(root.Path, path)
		}
		if !attached {
			return tc.CheckPathPolicy(ctx, path, mutation)
		}
		return tc.CheckPolicyTargets(ctx, []agentpolicy.Target{{RootID: rootID, Path: path, Mutation: mutation}})
	}
	abs, root, err := projectroot.ResolveAbs(tc.Source.Roots, rootID, path)
	if err != nil {
		return err
	}
	return tc.CheckPolicyTargets(ctx, []agentpolicy.Target{{RootID: root.ID, Path: abs, Mutation: mutation}})
}
