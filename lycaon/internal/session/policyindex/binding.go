package policyindex

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

// Bind freezes the policy delivered on the request that proposed an operation.
func (m *Service) Bind(ctx context.Context, sess *api.Session, messages []api.Message) (func(context.Context, string, string, bool) error, error) {
	if m == nil || m.roots == nil || sess == nil {
		return nil, nil
	}
	delivered := m.ManifestFor(messages).Clone()
	roots, err := m.roots.Roots(ctx, sess)
	if err != nil {
		return nil, err
	}
	roots = slices.Clone(roots)
	identity := *sess
	return func(ctx context.Context, rootID, path string, mutation bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		root, ok := projectroot.RootRefByID(roots, rootID)
		if !ok {
			return policyUnavailable(ctx, path)
		}
		rel := path
		if filepath.IsAbs(path) {
			var err error
			rel, err = filepath.Rel(root.Path, path)
			if err != nil {
				return policyUnavailable(ctx, path)
			}
		}
		scope := agentpolicy.Scope{RootID: root.ID, RootPath: root.Path, Path: filepath.ToSlash(rel)}
		if err := m.Observe(ctx, identity.ID, scope); err != nil {
			return policyUnavailable(ctx, path)
		}
		if !mutation {
			return nil
		}
		current, _, err := m.Current(ctx, &identity, []agentpolicy.Scope{scope})
		if err != nil {
			return policyUnavailable(ctx, path)
		}
		if !delivered.Covers(current) {
			return &toolrejection.ToolReject{Code: "POLICY_CONTEXT_CHANGED", Data: map[string]any{"root_id": root.ID, "path": scope.Path}}
		}
		return nil
	}, nil
}

func policyUnavailable(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return &toolrejection.ToolReject{Code: "POLICY_CONTEXT_UNAVAILABLE", Data: map[string]any{"path": path}}
}
