package project

import (
	"context"
	"strings"
)

// ScopeLookup resolves project event scopes through a registry.
type ScopeLookup struct {
	Registry Registry
}

// ResolveProject returns the canonical ID, root, and registry membership.
func (l ScopeLookup) ResolveProject(ctx context.Context, projectIDOrDir string) (string, string, bool, error) {
	projectIDOrDir = strings.TrimSpace(projectIDOrDir)
	if projectIDOrDir == "" {
		return "", "", false, nil
	}
	if l.Registry == nil {
		return projectIDOrDir, "", false, nil
	}
	if p, err := l.Registry.Get(ctx, projectIDOrDir); err == nil {
		return p.ID, PrimaryRootPath(p), true, nil
	}
	resolved, err := ResolveExistingDir(projectIDOrDir)
	if err != nil {
		return projectIDOrDir, "", false, nil //nolint:nilerr // Input may be a path.
	}
	projects, err := l.Registry.List(ctx)
	if err != nil {
		return "", "", false, err
	}
	if p := FindByRootPath(projects, resolved); p != nil {
		return p.ID, resolved, true, nil
	}
	return projectIDOrDir, resolved, false, nil
}
