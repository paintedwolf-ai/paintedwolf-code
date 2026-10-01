package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Manager) projectRow(ctx context.Context, sess *api.Session) (*project.Project, bool) {
	if m == nil || m.projects == nil || sess == nil {
		return nil, false
	}
	projectID := strings.TrimSpace(sess.ProjectID)
	if projectID == "" {
		return nil, false
	}
	p, err := m.projects.Get(ctx, projectID)
	if err != nil || p == nil {
		return nil, false
	}
	return p, true
}

// surfaceApplies requires a wired trust store.
func (m *Manager) surfaceApplies(ctx context.Context, surface string, sess *api.Session) bool {
	if m == nil || m.trustSurfaces == nil {
		return false
	}
	p, ok := m.projectRow(ctx, sess)
	if !ok {
		return false
	}
	return m.trustSurfaces.Applies(string(surface), *p)
}

// overlayProjectDir is the workspace path when project_settings applies.
func (m *Manager) overlayProjectDir(ctx context.Context, sess *api.Session) string {
	if m == nil || sess == nil {
		return ""
	}
	if !m.surfaceApplies(ctx, projectcontrib.SurfaceProjectSettings, sess) {
		return ""
	}
	projectDir, err := m.sessionActiveRootPath(ctx, sess)
	if err != nil {
		return ""
	}
	return projectDir
}

// overlayRootPaths returns roots for project settings.
func (m *Manager) overlayRootPaths(ctx context.Context, sess *api.Session) []string {
	return m.gatedOverlayRootPaths(ctx, projectcontrib.SurfaceProjectSettings, sess)
}

// promptOverlayRootPaths returns roots for project prompt files.
func (m *Manager) promptOverlayRootPaths(ctx context.Context, sess *api.Session) []string {
	return m.gatedOverlayRootPaths(ctx, projectcontrib.SurfacePromptOverrides, sess)
}

func (m *Manager) gatedOverlayRootPaths(ctx context.Context, surface string, sess *api.Session) []string {
	if m == nil || sess == nil {
		return nil
	}
	if !m.surfaceApplies(ctx, surface, sess) {
		return nil
	}
	return m.ungatedOverlayRootPaths(ctx, sess)
}
