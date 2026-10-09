package catalog

import (
	"context"
	"errors"
	"fmt"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

type SessionReader interface {
	Get(context.Context, string) (*api.Session, error)
}

// Registry merges request-scoped tiers with the explicitly supplied host registry.
func (r Resolver) Registry(ctx context.Context, projectDir, sessionID string) (*workflowdef.Registry, error) {
	base, _, err := r.Resolve(ctx, projectDir, sessionID)
	if err != nil {
		return nil, err
	}
	if r.Overlay == nil {
		return base, nil
	}
	overlay := r.Overlay.All()
	if len(overlay) == 0 {
		return base, nil
	}
	entries := base.All()
	merged := make(map[string]workflowdef.Manifest, len(entries)+len(overlay))
	for key, manifest := range entries {
		merged[key] = manifest
	}
	for key, manifest := range overlay {
		merged[key] = manifest
	}
	return workflowdef.NewRegistry(merged), nil
}

func (r Resolver) ForSession(ctx context.Context, projectDir, sessionID, workflowID, version string) (workflowdef.Manifest, error) {
	registry, err := r.Registry(ctx, projectDir, sessionID)
	if err != nil {
		return workflowdef.Manifest{}, err
	}
	return registry.Get(workflowID, version)
}

func (r Resolver) SessionScoped(ctx context.Context, projectDir, sessionID, workflowID, version string) bool {
	_, scopes, err := r.Resolve(ctx, projectDir, sessionID)
	return err == nil && scopes[workflowdef.ManifestKey(workflowID, version)] == string(api.WorkflowScopeSession)
}

func (r Resolver) ForRun(ctx context.Context, run *api.WorkflowRun) (workflowdef.Manifest, error) {
	if run == nil {
		return workflowdef.Manifest{}, fmt.Errorf("workflow run required")
	}
	manifest, err := r.ForSession(ctx, r.ProjectDirForRun(ctx, run), run.SessionID, run.WorkflowID, run.WorkflowVersion)
	if errors.Is(err, workflowdef.ErrUnknownWorkflow) {
		return workflowdef.Manifest{}, &runstate.WorkflowVersionUnavailableError{WorkflowID: run.WorkflowID, Version: run.WorkflowVersion}
	}
	return manifest, err
}

func (r Resolver) ProjectDirForRun(ctx context.Context, run *api.WorkflowRun) string {
	if run == nil {
		return ""
	}
	return r.ProjectDir(ctx, run.SessionID)
}

func (r Resolver) ProjectDir(ctx context.Context, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	if r.Sessions != nil {
		if session, err := r.Sessions.Get(ctx, sessionID); err == nil && session != nil && strings.TrimSpace(session.WorkspacePath) != "" {
			return strings.TrimSpace(session.WorkspacePath)
		}
	}
	if r.ProjectDirFallback != nil {
		if dir, err := r.ProjectDirFallback(ctx, sessionID); err == nil {
			return strings.TrimSpace(dir)
		}
	}
	return ""
}
