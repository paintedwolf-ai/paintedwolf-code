package session

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
)

// ResolvePromptToolProfile returns the tool profile Prompt would use for sessionID.
func (m *Manager) ResolvePromptToolProfile(ctx context.Context, sessionID string) (string, error) {
	if m == nil || m.store == nil {
		return "", fmt.Errorf("session manager not configured")
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return m.promptToolProfile(ctx, sess)
}

// WarmPostureOverlay validates and caches a posture overlay.
func (m *Manager) WarmPostureOverlay(projectDir string) error {
	if m == nil || m.postures == nil {
		return nil
	}
	_, err := m.loadPostureOverlays([]string{projectDir})
	return err
}

// WarmPostureOverlayForProject warms an admitted posture overlay.
func (m *Manager) WarmPostureOverlayForProject(p *project.Project, projectDir string) error {
	if m == nil || m.postures == nil {
		return nil
	}
	if p == nil || m.trustSurfaces == nil {
		return nil
	}
	if !m.trustSurfaces.Applies(projectcontrib.SurfaceProjectSettings, *p) {
		return nil
	}
	_, err := m.loadPostureOverlays([]string{projectDir})
	return err
}

// effectivePostures merges applicable posture overlays.
func (m *Manager) effectivePostures(ctx context.Context, sess *api.Session) (*PostureRegistry, error) {
	if m == nil || m.postures == nil {
		return nil, nil
	}
	if sess == nil {
		return m.postures, nil
	}
	if !m.surfaceApplies(ctx, projectcontrib.SurfaceProjectSettings, sess) {
		return m.postures, nil
	}
	projectDirs := m.overlayRootPaths(ctx, sess)
	if len(projectDirs) == 0 {
		return m.postures, nil
	}
	return m.loadPostureOverlays(projectDirs)
}

func (m *Manager) loadPostureOverlays(dirs []string) (*PostureRegistry, error) {
	if err := settingsoverlay.CheckFormats(dirs); err != nil {
		return nil, err
	}
	key := project.OverlayCacheKey(dirs)
	if key == "" {
		return m.postures, nil
	}
	if cached, ok := m.postureOverlay.Load(key); ok {
		return cached, nil
	}
	merged, err := MergePostureOverlays(m.postures, dirs)
	if err != nil {
		return nil, err
	}
	actual, _ := m.postureOverlay.LoadOrStore(key, merged)
	return actual, nil
}

// promptToolProfile uses the workflow manifest, then the agent, defaulting to coordinator.
func (m *Manager) promptToolProfile(ctx context.Context, sess *api.Session) (string, error) {
	coordinatorProfile := ""
	if m.workflows != nil {
		if manifest, ok := m.workflows.Policy.ActiveManifest(ctx, sess.ID); ok {
			coordinatorProfile = manifest.CoordinatorProfile
		}
	}
	agents := m.agents
	if view := m.Catalog().ViewForSession(ctx, sess); view != nil {
		agents = view
	}
	return ResolveToolProfile(sess, agents, coordinatorProfile), nil
}
