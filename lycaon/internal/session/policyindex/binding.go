package policyindex

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Bind freezes the policy delivered on the request that proposed an operation.
func (m *Service) Bind(ctx context.Context, sess *api.Session, messages []api.Message) (tools.AgentPolicyCheck, error) {
	if m == nil || m.roots == nil || sess == nil {
		return nil, nil
	}
	delivered := m.ManifestFor(messages).Clone()
	roots, err := m.sessionRoots(ctx, sess)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return func(ctx context.Context, targets []agentpolicy.Target) error {
			for _, target := range targets {
				if target.Mutation {
					return policyUnavailable(ctx, target.Path)
				}
			}
			return ctx.Err()
		}, nil
	}
	roots = slices.Clone(roots)
	sessionID := sess.ID
	return func(ctx context.Context, targets []agentpolicy.Target) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var scopes, mutations []agentpolicy.Scope
		for _, target := range targets {
			root, ok := projectroot.RootRefByID(roots, target.RootID)
			if !ok {
				if target.Mutation {
					return policyUnavailable(ctx, target.Path)
				}
				continue
			}
			rel := target.Path
			if filepath.IsAbs(rel) {
				var err error
				rel, err = filepath.Rel(root.Path, rel)
				if err != nil {
					if target.Mutation {
						return policyUnavailable(ctx, target.Path)
					}
					continue
				}
			}
			scope := agentpolicy.Scope{RootID: root.ID, RootPath: root.Path, Path: filepath.ToSlash(rel)}
			scopes = append(scopes, scope)
			if target.Mutation {
				mutations = append(mutations, scope)
			}
		}
		if len(scopes) == 0 {
			return nil
		}
		current, status, err := m.ObserveScopes(ctx, sessionID, uniqueScopes(scopes))
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if failedMutation(err, mutations) {
			return policyUnavailable(ctx, mutations[0].Path)
		}
		var limit *scopeLimitError
		if errors.As(err, &limit) && len(mutations) > 0 {
			return &toolrejection.ToolReject{Code: "POLICY_CONTEXT_CHANGED", Data: map[string]any{"reason": "scope_limit", "scope_count": limit.Count, "scope_limit": limit.Limit, "path": mutations[0].Path}}
		}
		for _, scope := range mutations {
			if status != StatusDisabled && !policyScopeMatches(delivered, current, scope) {
				return &toolrejection.ToolReject{Code: "POLICY_CONTEXT_CHANGED", Data: map[string]any{"root_id": scope.RootID, "path": scope.Path}}
			}
		}
		return ctx.Err()
	}, nil
}

func policyUnavailable(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return &toolrejection.ToolReject{Code: "POLICY_CONTEXT_UNAVAILABLE", Data: map[string]any{"path": path}}
}

// Policy removal changes the guidance just as a source edit does.
func policyScopeMatches(delivered, current agentpolicy.Manifest, scope agentpolicy.Scope) bool {
	root, err := filepath.EvalSymlinks(scope.RootPath)
	if err != nil {
		return false
	}
	path := filepath.ToSlash(filepath.Clean(scope.Path))
	present := make(map[agentpolicy.Revision]bool, len(current.Files))
	applicable := agentpolicy.Manifest{}
	for _, file := range current.Files {
		if file.RootID == scope.RootID && file.RootPath == root && policyPathApplies(file.Path, path) {
			present[file] = true
			applicable.Files = append(applicable.Files, file)
		}
	}
	if !delivered.Covers(applicable) {
		return false
	}
	for _, old := range delivered.Files {
		if old.RootID != scope.RootID || old.RootPath != root {
			continue
		}
		if policyPathApplies(old.Path, path) && !present[old] {
			return false
		}
	}
	return true
}

func policyPathApplies(policy, path string) bool {
	parent := filepath.ToSlash(filepath.Dir(policy))
	return policy == settingsoverlay.Rel("AGENTS.md") || parent == "." || path == parent || strings.HasPrefix(path, parent+"/")
}
