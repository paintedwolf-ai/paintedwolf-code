package profiles

import (
	"context"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
)

// WarmPostureOverlay validates and caches a posture overlay.
func (m *Service) WarmPostureOverlay(projectDir string) error {
	if m == nil || m.postures == nil {
		return nil
	}
	_, err := m.loadPostureOverlays([]string{projectDir})
	return err
}

// WarmPostureOverlayForProject warms an admitted posture overlay.
func (m *Service) WarmPostureOverlayForProject(p *project.Project, projectDir string) error {
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

// EffectivePostures merges applicable posture overlays.
func (m *Service) EffectivePostures(ctx context.Context, sess *api.Session) (*PostureRegistry, error) {
	if m == nil || m.postures == nil {
		return nil, nil
	}
	if sess == nil {
		return m.postures, nil
	}
	if !m.workspace.Applies(ctx, projectcontrib.SurfaceProjectSettings, sess) {
		return m.postures, nil
	}
	projectDirs := m.workspace.SettingsRoots(ctx, sess)
	if len(projectDirs) == 0 {
		return m.postures, nil
	}
	return m.loadPostureOverlays(projectDirs)
}

func (m *Service) loadPostureOverlays(dirs []string) (*PostureRegistry, error) {
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
