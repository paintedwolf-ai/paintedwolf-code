package catalog

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetCatalogViewCache supplies project catalog views.
func (m *Service) SetCatalogViewCache(cache *catalogview.Cache) {
	if m == nil {
		return
	}
	m.viewCache = cache
}

// DeviceView returns the active device catalog view.
func (m *Service) DeviceView(ctx context.Context) *catalogview.View {
	if m == nil || m.viewCache == nil {
		return nil
	}
	eff := m.DeviceCatalog(ctx)
	if eff == nil {
		return nil
	}
	view, committed, err := m.viewCache.ForCommitted(ctx, eff)
	if err != nil {
		m.logViewFailOnce("device", err)
		return nil
	}
	if committed != eff {
		extpacks.ReplaceActiveFrom(eff, committed)
	}
	return view
}

// PublishedDeviceView compiles the process catalog that is already published.
// Boot warm uses this so it cannot re-resolve desired state (and take the
// package-cache lock) before the server listens.
func (m *Service) PublishedDeviceView(ctx context.Context) *catalogview.View {
	if m == nil || m.viewCache == nil {
		return nil
	}
	eff := extpacks.Active()
	if eff == nil {
		eff = m.bootCatalog
	}
	if eff == nil {
		return nil
	}
	view, committed, err := m.viewCache.ForCommitted(ctx, eff)
	if err != nil {
		m.logViewFailOnce("device-published", err)
		return nil
	}
	if committed != eff {
		extpacks.ReplaceActiveFrom(eff, committed)
	}
	return view
}

func (m *Service) withProjectOARConfig(ctx context.Context, projectID string, view *catalogview.View) *catalogview.View {
	if m == nil || view == nil || view.Rules == nil || m.projects == nil {
		return view
	}
	p, err := m.projects.Get(ctx, projectID)
	if err != nil || p == nil {
		return view
	}
	rules, err := oar.ConfiguredProjectRuleSet(view.Rules, project.PrimaryRootPath(p))
	if err != nil {
		m.logViewFailOnce(projectID+":oar-config", err)
		return view
	}
	if rules == view.Rules {
		return view
	}
	return view.WithRules(rules)
}

// ViewForProject returns the scoped view or a logged device fallback.
func (m *Service) ViewForProject(ctx context.Context, projectID string) *catalogview.View {
	if m == nil {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return m.DeviceView(ctx)
	}
	if m.viewCache == nil {
		return m.DeviceView(ctx)
	}
	eff, err := m.EffectiveCatalogForProject(ctx, projectID)
	if err != nil || eff == nil {
		m.logViewFailOnce(projectID, err)
		return m.DeviceView(ctx)
	}
	view, committed, err := m.viewCache.ForCommitted(ctx, eff)
	if err != nil {
		m.logViewFailOnce(projectID+":"+eff.Revision, err)
		return m.DeviceView(ctx)
	}
	if committed != eff {
		m.rememberCommittedCatalog(projectID, eff, committed)
	}
	return m.withProjectOARConfig(ctx, projectID, view)
}

// rememberCommittedCatalog caches a successor without changing device state.
func (m *Service) rememberCommittedCatalog(projectID string, from, to *extpacks.EffectiveCatalog) {
	if m == nil || from == to || to == nil {
		return
	}
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()
	if ent, ok := m.effectiveCatalogCache.Load(projectID); ok && ent.catalog == from {
		ent.catalog = to
		m.effectiveCatalogCache.Store(projectID, ent)
	}
}

// ViewForSession returns the catalog view for sess's project (device when unset).
func (m *Service) ViewForSession(ctx context.Context, sess *api.Session) *catalogview.View {
	if m == nil {
		return nil
	}
	if sess == nil {
		return m.DeviceView(ctx)
	}
	eff, err := m.effectiveCatalogForSession(ctx, sess)
	if err != nil || eff == nil {
		m.logViewFailOnce(sess.ProjectID, err)
		return m.DeviceView(ctx)
	}
	if m.viewCache == nil {
		return m.DeviceView(ctx)
	}
	view, _, err := m.viewCache.ForCommitted(ctx, eff)
	if err != nil {
		m.logViewFailOnce(sess.ProjectID+":"+eff.Revision, err)
		return m.DeviceView(ctx)
	}
	return m.withProjectOARConfig(ctx, sess.ProjectID, view)
}

// ViewForSessionID resolves a view from a session id for OAR/toolhost evaluators.
func (m *Service) ViewForSessionID(ctx context.Context, sessionID string) *catalogview.View {
	if m == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || m.store == nil {
		return m.DeviceView(ctx)
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return m.DeviceView(ctx)
	}
	return m.ViewForSession(ctx, sess)
}

func (m *Service) logViewFailOnce(key string, err error) {
	if m == nil {
		return
	}
	if _, loaded := m.viewFailLog.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	prefix := key
	if len(prefix) > 24 {
		prefix = prefix[:24]
	}
	if err != nil {
		slog.Warn("catalog view resolve failed; using device view", "key", prefix, "error", err)
		return
	}
	slog.Warn("catalog view resolve failed; using device view", "key", prefix)
}
