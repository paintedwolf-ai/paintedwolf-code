package tools

import (
	"context"
	"path/filepath"
	"strings"
)

// AgentPolicyCheck is frozen from the request that proposed the operation.
type AgentPolicyCheck func(context.Context, string, string, bool) error

type agentPolicyKey struct{}

func WithAgentPolicy(ctx context.Context, check AgentPolicyCheck) context.Context {
	return context.WithValue(ctx, agentPolicyKey{}, check)
}

// CheckPathPolicy checks the most specific attached root of a resolved path.
func (tc ToolContext) CheckPathPolicy(ctx context.Context, path string, mutation bool) error {
	check, _ := ctx.Value(agentPolicyKey{}).(AgentPolicyCheck)
	if check == nil || path == "" {
		return nil
	}
	var rootID, rootPath string
	for _, root := range tc.Source.Roots {
		rel, err := filepath.Rel(root.Path, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(root.Path) > len(rootPath) {
			rootID, rootPath = root.ID, root.Path
		}
	}
	if rootID == "" {
		return nil
	}
	return check(ctx, rootID, path, mutation)
}
