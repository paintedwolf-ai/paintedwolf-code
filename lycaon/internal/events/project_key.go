package events

import (
	"context"
	"strings"
)

// ProjectLookup resolves an event scope from an ID or root path.
type ProjectLookup interface {
	ResolveProject(ctx context.Context, projectIDOrDir string) (projectID, projectDir string, claimed bool, err error)
}

// ResolvedProjectID reports a claimed canonical project ID.
func ResolvedProjectID(ctx context.Context, lookup ProjectLookup, projectIDOrDir string) (string, bool) {
	projectIDOrDir = strings.TrimSpace(projectIDOrDir)
	if projectIDOrDir == "" || lookup == nil {
		return "", false
	}
	id, _, claimed, err := lookup.ResolveProject(ctx, projectIDOrDir)
	if err != nil || id == "" || !claimed {
		return "", false
	}
	return id, true
}

// PublishKeyFor resolves registered roots to project IDs. Invalid scopes are rejected by the hub.
func PublishKeyFor(ctx context.Context, lookup ProjectLookup, projectID, sessionID string) PublishKey {
	key := PublishKey{Project: strings.TrimSpace(projectID), Session: strings.TrimSpace(sessionID)}
	if lookup == nil {
		return key
	}
	if id, _, claimed, err := lookup.ResolveProject(ctx, key.Project); err == nil && claimed && id != "" {
		key.Project = id
	}
	return key
}

// resolveProjectDir returns the resolved primary root.
func resolveProjectDir(ctx context.Context, lookup ProjectLookup, projectIDOrDir string) string {
	projectIDOrDir = strings.TrimSpace(projectIDOrDir)
	if projectIDOrDir == "" || lookup == nil {
		return ""
	}
	_, dir, _, err := lookup.ResolveProject(ctx, projectIDOrDir)
	if err == nil {
		return dir
	}
	return ""
}
